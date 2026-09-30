// Command server is the CallGo.mn control-plane API.
//
// Wiring order: config → logger → postgres (+migrations, seed) → live hub →
// webhooks dispatcher (tee bus) → billing → telephony (LiveKit or mock) →
// recordings (observed bus) → identity → integrations (SMS, post-call,
// callbacks) → lexicon / knowledge → campaign engine → HTTP API →
// background jobs → optional simulator → graceful shutdown.
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

	lkproto "github.com/livekit/protocol/livekit"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/analytics"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/callbacks"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/campaign"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/config"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/crm"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/csvimport"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/httpapi"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/identity"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/knowledge"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/lexicon"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/live"
	lk "github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/llmtest"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/mailer"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/objstore"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/payments/mock"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/payments/qpay"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/postcall"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/recording"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/sms"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks"
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

	// ---- live hub + event bus chain --------------------------------------
	// Publishers write to `bus`: recording observer → webhooks tee → hub.
	hub := live.NewHub(log, live.HubOptions{})
	defer hub.Close()

	dispatcher := webhooks.New(store, nil, webhooks.Config{}, hub, log)
	tee := webhooks.NewTeeBus(ctx, hub, dispatcher, log)
	go dispatcher.Run(ctx)

	// ---- billing -----------------------------------------------------------
	store.SetPlanLookup(billing.Plans.Get)
	var provider domain.PaymentProvider
	var extraProviders []domain.PaymentProvider
	// The mock provider settles on POST /api/billing/webhooks/mock?payment_id=…&paid=true,
	// or by itself after a short while outside production (demo flow).
	mockProvider := mock.New()
	if cfg.Env != "production" {
		mockProvider.AutoPayAfter = 30 * time.Second
	}
	if qcfg := qpay.FromEnv(os.Getenv); qcfg.Configured() {
		provider = qpay.New(qcfg)
		if cfg.Env != "production" {
			extraProviders = append(extraProviders, mockProvider)
		}
		log.Info().Msg("payments: QPay configured")
	} else {
		provider = mockProvider
		log.Warn().Msg("payments: QPay not configured (QPAY_*) — MOCK provider only")
	}
	billingSvc := billing.New(store, store, provider, tee, billing.Config{
		ProviderCosts: billing.ProviderCosts{
			LLMPer1kTokensMNT: float64(cfg.CostLLMPer1kTokensMNT),
			STTPerMinMNT:      float64(cfg.CostSTTPerMinMNT),
			TTSPer1kCharsMNT:  float64(cfg.CostTTSPer1kCharsMNT),
			SMSPerMsgMNT:      float64(cfg.CostSMSPerMsgMNT),
		},
		ExtraProviders: extraProviders,
	}, log)
	if _, err := billingSvc.StartTrial(ctx, org.ID); err != nil {
		log.Warn().Err(err).Msg("billing: default org trial")
	}

	// ---- campaign engine (declared early so the mock bridge can reach it) ---
	var engine *campaign.Engine
	var bus domain.EventBus = tee
	var callEndedHooks []func(ctx context.Context, call *domain.Call)

	// ---- telephony ---------------------------------------------------------
	var tel domain.Telephony
	var egress recording.Egress
	if cfg.MockTelephony {
		log.Warn().Msg("telephony: MOCK mode (CALLGO_MOCK_TELEPHONY=true) — no real SIP calls")
		bridge := &mockBridge{store: store, bus: func() domain.EventBus { return bus }, log: log,
			engine: func() *campaign.Engine { return engine }, hooks: func() []func(context.Context, *domain.Call) { return callEndedHooks }}
		mock := lk.NewMock(lk.MockOptions{
			RingDelay:    3 * time.Second,
			CallDuration: 25 * time.Second,
			OnEvent:      bridge.onEvent,
		})
		defer mock.Close()
		tel, egress = mock, mock
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
		tel, egress = client, client
	}

	// ---- recordings --------------------------------------------------------
	var (
		objs       domain.ObjectStore
		localFiles http.Handler
		s3Target   *lk.S3Target
	)
	switch cfg.RecordingsDriver {
	case "s3":
		s3, err := objstore.NewS3(objstore.S3Config{
			Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, UseSSL: cfg.S3UseSSL,
			PublicBaseURL: cfg.S3PublicURL,
		})
		if err != nil {
			return fmt.Errorf("recordings: s3 store: %w", err)
		}
		if err := s3.EnsureBucket(ctx); err != nil {
			log.Warn().Err(err).Str("bucket", cfg.S3Bucket).Msg("recordings: bucket not ready")
		}
		objs = s3
		egressEndpoint := cfg.S3EgressEndpoint
		if egressEndpoint == "" {
			egressEndpoint = cfg.S3Endpoint
		}
		s3Target = &lk.S3Target{
			Endpoint: egressEndpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey, Secret: cfg.S3SecretKey, ForcePathStyle: true,
		}
	default:
		local, err := objstore.NewLocal(cfg.RecordingsDir, objstore.LocalSigner([]byte(cfg.JWTSecret), ""))
		if err != nil {
			return fmt.Errorf("recordings: local store (%s): %w", cfg.RecordingsDir, err)
		}
		objs = local
		localFiles = objstore.ServeLocal(cfg.RecordingsDir)
	}
	recSvc := recording.New(store, store, egress, objs, billingSvc, tee, recording.Config{
		Enabled: cfg.RecordingsEnabled, S3: s3Target, RetentionDays: cfg.RecordingRetentionDays,
	}, log)
	bus = recSvc.ObserveBus(tee)
	defer recSvc.Wait()
	go recSvc.RunRetentionEvery(ctx, 24*time.Hour)
	log.Info().Bool("enabled", cfg.RecordingsEnabled).Str("driver", cfg.RecordingsDriver).Msg("recordings configured")

	// ---- identity ----------------------------------------------------------
	var mail domain.Mailer
	if cfg.SMTPHost != "" {
		mail = mailer.NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom)
	} else {
		log.Warn().Msg("mail: SMTP_HOST not set — emails are logged only")
		mail = mailer.NewLog(log)
	}
	mailQueue := mailer.NewQueue(mail, log, 256)
	defer func() {
		qctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mailQueue.Close(qctx)
	}()
	idSvc := identity.New(store, store, mailQueue, billingSvc, identity.Config{
		AppURL: cfg.AppURL, AccessTTL: cfg.AccessTTL, RefreshTTL: cfg.RefreshTTL,
		AllowSignup: cfg.AllowSignup, JWTSecret: []byte(cfg.JWTSecret),
	}, log)
	idSvc.Limits = billingSvc.Limits
	idSvc.HasFeature = billingSvc.HasFeature
	idSvc.Subscription = store.GetSubscription

	// ---- integrations: SMS, callbacks, post-call actions -------------------
	smsSvc := sms.New(store,
		sms.SettingsLoaderFunc(func(ctx context.Context, orgID uuid.UUID) (map[string]any, error) {
			o, err := store.GetOrg(ctx, orgID)
			if err != nil {
				return nil, err
			}
			return o.Settings, nil
		}),
		sms.DefaultFactory(store.DecryptBytes), billingSvc, billingSvc, log,
		sms.WithCrypto(sms.Crypto{Encrypt: store.EncryptBytes, Decrypt: store.DecryptBytes}))

	sched := callbacks.New(store, store, store, store, store, tel, billingSvc, bus,
		callbacks.Config{RingTimeout: cfg.SIP.RingTimeout}, log).WithOrgs(store)
	go func() {
		if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error().Err(err).Msg("callback scheduler stopped")
		}
	}()

	postcallRunner := postcall.New(store, store, store, smsSvc, dispatcher, sched, log,
		postcall.WithMarkDone(func(ctx context.Context, callID uuid.UUID) error {
			c, err := store.GetCall(ctx, callID)
			if err != nil {
				return err
			}
			if c.Metadata == nil {
				c.Metadata = map[string]any{}
			}
			c.Metadata[postcall.MetaDone] = true
			return store.UpdateCall(ctx, c)
		}))

	analyticsStore := analytics.New(pool)

	callEndedHooks = []func(ctx context.Context, call *domain.Call){
		func(ctx context.Context, c *domain.Call) {
			if err := billingSvc.RecordCall(ctx, c); err != nil {
				log.Warn().Err(err).Str("call", c.ID.String()).Msg("billing: record call")
			}
		},
		func(ctx context.Context, c *domain.Call) {
			if err := postcallRunner.OnCallEnded(ctx, c); err != nil {
				log.Warn().Err(err).Str("call", c.ID.String()).Msg("post-call actions")
			}
		},
		sched.OnCallEnded,
	}

	// ---- domain services ---------------------------------------------------
	lex := lexicon.NewService(store, bus, log)

	if cfg.KnowledgeFakeEmbeddings {
		log.Warn().Msg("knowledge: FAKE embeddings enabled (CALLGO_EMBED_FAKE=true) — dev only")
	}
	kb := knowledge.NewService(store, store,
		knowledge.DefaultEmbedderFactory(&http.Client{Timeout: 60 * time.Second}),
		knowledge.Options{FakeEmbeddings: cfg.KnowledgeFakeEmbeddings}, log)
	defer kb.Close()

	engine = campaign.NewEngine(store, store, store, store, store, store, tel, bus, campaign.Options{
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
		DNC:           store,
		CampaignStats: store,
		Knowledge:     kb,
		KnowledgeRepo: store,
		Chunks:        store,
		Telephony:     tel,
		Bus:           hub,
		Live:          hub,
		Campaigns:     engine,
		TargetParser:  csvAdapter{},
		ContactParser: csvAdapter{},
		LexiconEngine: lexAdapter{lex},
		LLMTester:     llmtest.NewProxy(cfg.AgentWorkerURL, &http.Client{Timeout: 30 * time.Second}),
		Ready:         pool.Ping,

		Identity: httpapi.IdentityDeps{Svc: idSvc, HasFeature: billingSvc.HasFeature},
		Billing: httpapi.BillingDeps{Svc: billingSvc, Audit: func(ctx context.Context, orgID uuid.UUID, actorID *uuid.UUID, action, targetType, targetID string, meta map[string]any) {
			if err := idSvc.Append(ctx, &domain.AuditEntry{OrgID: orgID, ActorID: actorID, Action: action, TargetType: targetType, TargetID: targetID, Meta: meta}); err != nil {
				log.Warn().Err(err).Str("action", action).Msg("audit append failed")
			}
		}},
		Recordings: httpapi.RecordingDeps{Svc: recSvc, LocalFiles: localFiles, LocalSecret: []byte(cfg.JWTSecret), HasFeature: billingSvc.HasFeature, Audit: idSvc},
		Handoff: httpapi.HandoffDeps{
			Calls: store, SetHandoff: store.SetCallHandoff, LiveKitURL: firstNonEmpty(cfg.LiveKitPublicURL, cfg.LiveKit.URL),
			HasFeature: billingSvc.HasFeature, Bus: bus, Audit: idSvc,
			UserName: func(ctx context.Context, userID uuid.UUID) (string, error) {
				u, err := store.GetUser(ctx, userID)
				if err != nil {
					return "", err
				}
				return u.Name, nil
			},
		},
		Integrations: httpapi.IntegrationsDeps{Repo: store, Webhooks: dispatcher, SMS: smsSvc, Orgs: store, Calls: store, HasFeature: billingSvc.HasFeature, Audit: idSvc},
		Analytics:    httpapi.AnalyticsDeps{Store: analyticsStore, Orgs: store, HasFeature: billingSvc.HasFeature},
		Routing:      httpapi.RoutingDeps{Numbers: store, Profiles: store, Orgs: store, Audit: idSvc},
		Callbacks:    httpapi.CallbackDeps{Sched: sched, Repo: store, Audit: idSvc},
		Admin: httpapi.AdminDeps{
			Identity: store, Billing: store, Users: store, Calls: store, Plans: billing.Plans.Get,
			SetSubscription: billingSvc.SetSubscription, MarkInvoicePaid: billingSvc.MarkPaidManually, Audit: idSvc,
		},

		Entitlements: billingSvc,
		OrgStatus: func(ctx context.Context, orgID uuid.UUID) (domain.OrgStatus, error) {
			o, err := store.GetOrg(ctx, orgID)
			if err != nil {
				return "", err
			}
			return o.Status, nil
		},
		APIKeys: idSvc,
		EgressWebhook: func(ctx context.Context, ev *lkproto.WebhookEvent) (bool, error) {
			return recording.HandleEgressWebhook(ctx, recSvc, ev)
		},
		CallEndedHooks: callEndedHooks,
		SetHandoff:     store.SetCallHandoff,
		CreateCallback: sched.CreateCallback,
	}, httpapi.Config{
		JWTSecret:        []byte(cfg.JWTSecret),
		AgentToken:       cfg.AgentToken,
		LiveKitAPIKey:    cfg.LiveKit.WebhookKeyPair.APIKey,
		LiveKitAPISecret: cfg.LiveKit.WebhookKeyPair.APISecret,
		AllowSignup:      cfg.AllowSignup,
		CORSOrigins:      cfg.CORSOrigins,
	}, log)

	// ---- background billing jobs --------------------------------------------
	go runEvery(ctx, 15*time.Minute, func(ctx context.Context) {
		if n, err := billingSvc.RunPeriodRollover(ctx); err != nil {
			log.Error().Err(err).Msg("billing: period rollover")
		} else if n > 0 {
			log.Info().Int("subscriptions", n).Msg("billing: periods rolled over")
		}
		if n, err := billingSvc.RunDunning(ctx); err != nil {
			log.Error().Err(err).Msg("billing: dunning")
		} else if n > 0 {
			log.Info().Int("subscriptions", n).Msg("billing: dunning applied")
		}
	})

	// ---- optional simulator -----------------------------------------------
	if cfg.Simulator {
		log.Warn().Msg("call simulator ENABLED (CALLGO_SIMULATOR=true) — generating fake calls")
		sim := live.NewSimulator(store, store, bus, org.ID, live.SimOptions{Interval: cfg.SimulatorInterval, Logger: log})
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

func (csvAdapter) ParseTargets(r io.Reader, filename string) (httpapi.ParsedTargets, error) {
	res, err := csvimport.ParseTargetsFile(r, filename, csvimport.Options{})
	if err != nil {
		return httpapi.ParsedTargets{}, err
	}
	return httpapi.ParsedTargets{Targets: res.Targets, Skipped: res.Skipped, Errors: rowErrors(res.Errors)}, nil
}

func (csvAdapter) ParseContacts(r io.Reader, filename string) (httpapi.ParsedContacts, error) {
	res, err := csvimport.ParseContactsFile(r, filename, csvimport.Options{})
	if err != nil {
		return httpapi.ParsedContacts{}, err
	}
	return httpapi.ParsedContacts{Contacts: res.Contacts, Skipped: res.Skipped, Errors: rowErrors(res.Errors)}, nil
}

func (csvAdapter) Preview(r io.Reader, filename string, n int) (httpapi.PreviewResult, error) {
	p, err := csvimport.PreviewFile(r, filename, n)
	if err != nil {
		return httpapi.PreviewResult{}, err
	}
	return httpapi.PreviewResult{Columns: p.Columns, Rows: p.Rows, Mapping: p.Mapping, Total: p.Total, Format: string(p.Format)}, nil
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
	bus    func() domain.EventBus
	log    zerolog.Logger
	engine func() *campaign.Engine
	// hooks run after a terminal call was persisted (metering, post-call
	// actions, callbacks) — the same hooks the HTTP API runs.
	hooks func() []func(ctx context.Context, call *domain.Call)
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
		b.mockOutcome(ctx, call)
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
	b.bus().Publish(ctx, domain.Event{
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
	if call.Status.IsTerminal() && b.hooks != nil {
		snap := *call
		for _, h := range b.hooks() {
			h(ctx, &snap)
		}
	}
}

// mockOutcome stands in for the agent's post-call analysis: campaign calls
// get a deterministic structured outcome (first terminal outcome, or the
// second one for numbers ending in an even digit) so the Excel export and
// stats can be demoed without a real LLM.
func (b *mockBridge) mockOutcome(ctx context.Context, call *domain.Call) {
	if call.CampaignID == nil {
		return
	}
	c, err := b.store.GetCampaign(ctx, *call.CampaignID)
	if err != nil || len(c.Outcomes) == 0 {
		return
	}
	var terminal []domain.CampaignOutcome
	for _, o := range c.Outcomes {
		if o.Terminal && o.Code != "no_contact" {
			terminal = append(terminal, o)
		}
	}
	if len(terminal) == 0 {
		return
	}
	pick := terminal[0]
	if n := len(call.ToNumber); n > 0 && (call.ToNumber[n-1]-'0')%2 == 0 && len(terminal) > 1 {
		pick = terminal[1]
	}
	call.Outcome = pick.Code
	call.OutcomeNote = "Mock telephony: " + pick.Label
}

func (b *mockBridge) finish(call *domain.Call, now time.Time) {
	call.EndedAt = &now
	if call.AnsweredAt != nil {
		call.DurationSec = int(now.Sub(*call.AnsweredAt).Seconds())
	}
}

// runEvery calls fn immediately and then every interval until ctx ends.
func runEvery(ctx context.Context, interval time.Duration, fn func(ctx context.Context)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		fctx, cancel := context.WithTimeout(ctx, interval)
		fn(fctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
