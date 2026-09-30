// Command server is the CallGo.mn control-plane API.
//
// Wiring order: config → logger → postgres (+migrations, seed) → live hub →
// telephony (LiveKit or mock) → lexicon → campaign engine → HTTP API →
// optional simulator → graceful shutdown.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/campaign"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/config"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/crm"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/csvimport"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/httpapi"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/lexicon"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/live"
	lk "github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/llmtest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := config.NewLogger(cfg)
	cfg.LogWarnings(log)
	log.Info().Str("env", cfg.Env).Str("addr", cfg.HTTPAddr).Msg("callgo backend starting")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---- persistence -------------------------------------------------------
	if err := crm.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	pool, err := crm.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer pool.Close()
	store := crm.New(pool, cfg.EncryptionKey)

	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	org, err := store.SeedDemo(ctx, cfg.AdminEmail, hash)
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	log.Info().Str("org", org.Slug).Str("admin", cfg.AdminEmail).Msg("default organisation ready")

	// ---- live hub ----------------------------------------------------------
	hub := live.NewHub(log, live.HubOptions{})
	defer hub.Close()

	// ---- campaign engine (declared early so the mock bridge can reach it) ---
	var engine *campaign.Engine

	// ---- telephony ---------------------------------------------------------
	var tel domain.Telephony
	if cfg.MockTelephony {
		log.Warn().Msg("telephony: MOCK mode (CALLGO_MOCK_TELEPHONY=true) — no real SIP calls")
		bridge := &mockBridge{store: store, bus: hub, log: log, engine: func() *campaign.Engine { return engine }}
		mock := lk.NewMock(lk.MockOptions{
			RingDelay:    3 * time.Second,
			CallDuration: 25 * time.Second,
			OnEvent:      bridge.onEvent,
		})
		defer mock.Close()
		tel = mock
	} else {
		client, err := lk.NewClient(lk.Config{
			URL:                        cfg.LiveKit.URL,
			APIKey:                     cfg.LiveKit.APIKey,
			APISecret:                  cfg.LiveKit.APISecret,
			AgentName:                  cfg.LiveKit.AgentName,
			SIPInboundAllowedAddresses: cfg.SIP.AllowedAddresses,
			SIPOutboundAddress:         fmt.Sprintf("%s:%d", cfg.SIP.AsteriskHost, cfg.SIP.AsteriskPort),
			SIPOutboundTransport:       cfg.SIP.Transport,
			SIPAuthUsername:            cfg.SIP.AuthUsername,
			SIPAuthPassword:            cfg.SIP.AuthPassword,
			SIPInboundAuthUsername:     cfg.SIP.InboundAuthUsername,
			SIPInboundAuthPassword:     cfg.SIP.InboundAuthPassword,
			RingTimeout:                cfg.SIP.RingTimeout,
			MaxCallDuration:            cfg.SIP.MaxCallDuration,
		}, log)
		if err != nil {
			return fmt.Errorf("livekit client: %w", err)
		}
		tel = client
	}

	// ---- domain services ---------------------------------------------------
	lex := lexicon.NewService(store, hub, log)

	engine = campaign.NewEngine(store, store, store, store, store, store, tel, hub, campaign.Options{
		PollInterval:         cfg.Campaign.PollInterval,
		RetryBackoff:         cfg.Campaign.RetryBackoff,
		RingTimeout:          cfg.SIP.RingTimeout,
		MaxGlobalConcurrency: cfg.Campaign.MaxConcurrency,
		MaxCallDuration:      cfg.SIP.MaxCallDuration,
	}, log)
	go func() {
		if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error().Err(err).Msg("campaign engine stopped")
		}
	}()

	// ---- HTTP API ----------------------------------------------------------
	handler := httpapi.New(httpapi.Deps{
		Org:           store,
		SIPNumber:     store,
		AgentProfile:  store,
		LLMConfig:     store,
		Call:          store,
		Contact:       store,
		Campaign:      store,
		Lexicon:       store,
		Telephony:     tel,
		Bus:           hub,
		Live:          hub,
		Campaigns:     engine,
		TargetParser:  csvAdapter{},
		ContactParser: csvAdapter{},
		LexiconEngine: lexAdapter{lex},
		LLMTester:     llmtest.NewProxy(cfg.AgentWorkerURL, &http.Client{Timeout: 30 * time.Second}),
		Ready:         pool.Ping,
	}, httpapi.Config{
		JWTSecret:        []byte(cfg.JWTSecret),
		AgentToken:       cfg.AgentToken,
		LiveKitAPIKey:    cfg.LiveKit.WebhookKeyPair.APIKey,
		LiveKitAPISecret: cfg.LiveKit.WebhookKeyPair.APISecret,
		AllowSignup:      cfg.AllowSignup,
		CORSOrigins:      cfg.CORSOrigins,
	}, log)

	// ---- optional simulator -----------------------------------------------
	if cfg.Simulator {
		log.Warn().Msg("call simulator ENABLED (CALLGO_SIMULATOR=true) — generating fake calls")
		sim := live.NewSimulator(store, store, hub, org.ID, live.SimOptions{Interval: cfg.SimulatorInterval, Logger: log})
		go func() {
			if err := sim.Run(ctx); err != nil {
				log.Error().Err(err).Msg("simulator stopped")
			}
		}()
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Msg("http listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	log.Info().Msg("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// ---- adapters between packages built independently -------------------------

// csvAdapter bridges internal/csvimport to the httpapi parser interfaces.
type csvAdapter struct{}

func (csvAdapter) ParseTargets(r io.Reader) (httpapi.ParsedTargets, error) {
	res, err := csvimport.ParseTargets(r, csvimport.Options{})
	if err != nil {
		return httpapi.ParsedTargets{}, err
	}
	return httpapi.ParsedTargets{Targets: res.Targets, Skipped: res.Skipped, Errors: rowErrors(res.Errors)}, nil
}

func (csvAdapter) ParseContacts(r io.Reader) (httpapi.ParsedContacts, error) {
	res, err := csvimport.ParseContacts(r, csvimport.Options{})
	if err != nil {
		return httpapi.ParsedContacts{}, err
	}
	return httpapi.ParsedContacts{Contacts: res.Contacts, Skipped: res.Skipped, Errors: rowErrors(res.Errors)}, nil
}

func rowErrors(in []csvimport.RowError) []httpapi.RowError {
	out := make([]httpapi.RowError, 0, len(in))
	for _, e := range in {
		out = append(out, httpapi.RowError{Row: e.Row, Message: e.Message})
	}
	return out
}

// lexAdapter bridges internal/lexicon to the httpapi Lexicon interface.
type lexAdapter struct{ svc *lexicon.Service }

func (a lexAdapter) Apply(ctx context.Context, orgID uuid.UUID, text string) (string, []httpapi.LexiconHit, error) {
	out, hits, err := a.svc.Apply(ctx, orgID, text)
	if err != nil {
		return "", nil, err
	}
	res := make([]httpapi.LexiconHit, 0, len(hits))
	for _, h := range hits {
		res = append(res, httpapi.LexiconHit{ID: h.ID, Wrong: h.Wrong, Correct: h.Correct})
	}
	return out, res, nil
}

func (a lexAdapter) Invalidate(orgID uuid.UUID) { a.svc.Invalidate(orgID) }

// mockBridge turns simulated telephony state changes into persisted call
// state and live events, mirroring what LiveKit webhooks + the agent worker
// do in a real deployment. It lets the Live Desk and campaign engine be
// exercised end-to-end without a SIP trunk.
type mockBridge struct {
	store  *crm.Store
	bus    domain.EventBus
	log    zerolog.Logger
	engine func() *campaign.Engine
}

func (b *mockBridge) onEvent(roomName, evt string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call, err := b.store.GetCallByRoom(ctx, roomName)
	if err != nil {
		b.log.Debug().Err(err).Str("room", roomName).Str("event", evt).Msg("mock event for unknown call")
		return
	}
	if call.Status.IsTerminal() {
		return
	}
	now := time.Now().UTC()
	var evType domain.EventType
	switch evt {
	case lk.MockEventRinging:
		call.Status = domain.StatusRinging
		evType = domain.EventCallRinging
	case lk.MockEventAnswered:
		call.Status = domain.StatusActive
		call.AnsweredAt = &now
		evType = domain.EventCallAnswered
	case lk.MockEventEnded:
		call.Status = domain.StatusCompleted
		call.EndReason = "hangup_customer"
		call.Summary = "Туршилтын дуудлага (mock telephony) амжилттай дууслаа."
		call.Sentiment = domain.SentimentNeutral
		call.Intent = "test"
		b.finish(call, now)
		evType = domain.EventCallEnded
	case lk.MockEventBusy:
		call.Status = domain.StatusBusy
		call.EndReason = "busy"
		b.finish(call, now)
		evType = domain.EventCallEnded
	case lk.MockEventNoAnswer:
		call.Status = domain.StatusNoAnswer
		call.EndReason = "no_answer"
		b.finish(call, now)
		evType = domain.EventCallEnded
	case lk.MockEventFailed:
		call.Status = domain.StatusFailed
		call.EndReason = "failed"
		b.finish(call, now)
		evType = domain.EventCallEnded
	default:
		return
	}
	if err := b.store.UpdateCall(ctx, call); err != nil {
		b.log.Error().Err(err).Str("call", call.ID.String()).Msg("mock bridge: update call")
		return
	}
	b.bus.Publish(ctx, domain.Event{
		Type:    evType,
		OrgID:   call.OrgID,
		CallID:  &call.ID,
		At:      now,
		Payload: map[string]any{"call": call, "endReason": call.EndReason},
	})
	if call.Status.IsTerminal() && call.CampaignID != nil {
		if eng := b.engine(); eng != nil {
			eng.OnCallEnded(ctx, call)
		}
	}
}

func (b *mockBridge) finish(call *domain.Call, now time.Time) {
	call.EndedAt = &now
	if call.AnsweredAt != nil {
		call.DurationSec = int(now.Sub(*call.AnsweredAt).Seconds())
	}
}
