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

	// ---- telephony ---------------------------------------------------------
	var tel domain.Telephony
	if cfg.MockTelephony {
		log.Warn().Msg("telephony: MOCK mode (CALLGO_MOCK_TELEPHONY=true) — no real SIP calls")
		tel = lk.NewMock(lk.MockOptions{})
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

	engine := campaign.NewEngine(store, store, store, store, store, tel, hub, campaign.Options{
		PollInterval:         cfg.Campaign.PollInterval,
		RetryBackoff:         cfg.Campaign.RetryBackoff,
		RingTimeout:          cfg.SIP.RingTimeout,
		MaxGlobalConcurrency: cfg.Campaign.MaxConcurrency,
	}, log)
	go func() {
		if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error().Err(err).Msg("campaign engine stopped")
		}
	}()

	// ---- HTTP API ----------------------------------------------------------
	handler := httpapi.New(httpapi.Deps{
		Orgs:          store,
		SIPNumbers:    store,
		AgentProfiles: store,
		LLMConfigs:    store,
		Calls:         store,
		Contacts:      store,
		Campaigns:     store,
		Lexicons:      store,
		Telephony:     tel,
		Bus:           hub,
		Hub:           hub,
		CampaignCtl:   engine,
		Targets:       csvAdapter{},
		Lexicon:       lexAdapter{lex},
		LLMTester:     llmtest.NewProxy(cfg.AgentWorkerURL, &http.Client{Timeout: 30 * time.Second}),
	}, httpapi.Config{
		JWTSecret:        []byte(cfg.JWTSecret),
		AgentToken:       cfg.AgentToken,
		LiveKitAPIKey:    cfg.LiveKit.APIKey,
		LiveKitAPISecret: cfg.LiveKit.APISecret,
		AllowSignup:      cfg.AllowSignup,
		CORSOrigins:      cfg.CORSOrigins,
	}, log)

	// ---- optional simulator -----------------------------------------------
	if cfg.Simulator {
		log.Warn().Msg("call simulator ENABLED (CALLGO_SIMULATOR=true) — generating fake calls")
		sim := live.NewSimulator(store, store, hub, org.ID, live.SimOptions{Interval: cfg.SimulatorInterval})
		go sim.Run(ctx)
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

type csvAdapter struct{}

func (csvAdapter) ParseTargets(r io.Reader) (httpapi.ParsedTargets, error) {
	res, err := csvimport.ParseTargets(r, csvimport.Options{})
	if err != nil {
		return httpapi.ParsedTargets{}, err
	}
	out := httpapi.ParsedTargets{Targets: res.Targets, Skipped: res.Skipped}
	for _, e := range res.Errors {
		out.Errors = append(out.Errors, httpapi.RowError{Row: e.Row, Message: e.Message})
	}
	return out, nil
}

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

var _ zerolog.Logger // keep import stable while adapters evolve
