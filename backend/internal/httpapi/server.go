// Package httpapi implements the CallGo.mn REST + WebSocket API described in
// docs/API.md, the LiveKit webhook receiver and the internal agent API.
package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type server struct {
	d   Deps
	cfg Config
	log zerolog.Logger
	now func() time.Time

	// bg tracks background work started by handlers (e.g. outbound dialing).
	bg sync.WaitGroup
}

// New builds the HTTP handler with every route of docs/API.md mounted.
func New(deps Deps, cfg Config, log zerolog.Logger) http.Handler {
	return newServer(deps, cfg, log).routes()
}

func newServer(deps Deps, cfg Config, log zerolog.Logger) *server {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = auth.DefaultTokenTTL
	}
	return &server{d: deps, cfg: cfg, log: log.With().Str("component", "httpapi").Logger(), now: func() time.Time { return time.Now().UTC() }}
}

func (s *server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.cfg.TrustedProxies > 0 {
		r.Use(middleware.ClientIPFromXFFTrustedProxies(s.cfg.TrustedProxies))
	} else {
		r.Use(middleware.ClientIPFromRemoteAddr)
	}
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	origins := s.cfg.CORSOrigins
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: origins,
		AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders: []string{"X-Request-Id"},
		MaxAge:         300,
	}))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		auth.WriteError(w, http.StatusMethodNotAllowed, "invalid", "method not allowed")
	})

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	r.Get("/readyz", s.readyz)

	r.Route("/internal/agent", func(r chi.Router) {
		r.Use(auth.RequireAgentToken(s.cfg.AgentToken))
		r.Get("/bootstrap", s.agentBootstrap)
		r.Post("/events", s.agentEvents)
		r.Post("/lexicon-hit", s.agentLexiconHit)
	})

	admin := auth.RequireRole(domain.RoleOwner, domain.RoleAdmin)

	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Post("/auth/register", s.register)
		r.Post("/livekit/webhook", s.livekitWebhook)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAuth(s.cfg.JWTSecret))
			r.Get("/auth/me", s.me)
			r.Get("/ws", s.ws)

			r.Get("/stats", s.stats)
			r.Get("/stats/daily", s.statsDaily)

			r.Route("/calls", func(r chi.Router) {
				r.Get("/", s.listCalls)
				r.Get("/active", s.activeCalls)
				r.Post("/dial", s.dial)
				r.Get("/{id}", s.getCall)
				r.Post("/{id}/hangup", s.hangup)
				r.Post("/{id}/transfer", s.transfer)
				r.Get("/{id}/recording", s.recording)
			})
			r.Patch("/turns/{id}", s.patchTurn)

			r.Route("/lexicon", func(r chi.Router) {
				r.Get("/", s.listLexicon)
				r.Post("/", s.createLexicon)
				r.Post("/apply", s.applyLexicon)
				r.Put("/{id}", s.updateLexicon)
				r.Delete("/{id}", s.deleteLexicon)
			})

			r.Route("/contacts", func(r chi.Router) {
				r.Get("/", s.listContacts)
				r.Post("/", s.upsertContact)
				r.Post("/import", s.importContacts)
				r.Get("/{id}", s.getContact)
				r.Delete("/{id}", s.deleteContact)
			})

			r.Route("/campaigns", func(r chi.Router) {
				r.Get("/", s.listCampaigns)
				r.Get("/{id}", s.getCampaign)
				r.With(admin).Post("/", s.createCampaign)
				r.With(admin).Post("/{id}/start", s.startCampaign)
				r.With(admin).Post("/{id}/pause", s.pauseCampaign)
				r.With(admin).Delete("/{id}", s.deleteCampaign)
			})

			r.Route("/sip-numbers", func(r chi.Router) {
				r.Get("/", s.listSIPNumbers)
				r.Get("/{id}", s.getSIPNumber)
				r.With(admin).Post("/", s.createSIPNumber)
				r.With(admin).Put("/{id}", s.updateSIPNumber)
				r.With(admin).Delete("/{id}", s.deleteSIPNumber)
				r.With(admin).Post("/{id}/provision", s.provisionSIPNumber)
			})

			r.Route("/agent-profiles", func(r chi.Router) {
				r.Get("/", s.listProfiles)
				r.Get("/{id}", s.getProfile)
				r.With(admin).Post("/", s.createProfile)
				r.With(admin).Put("/{id}", s.updateProfile)
				r.With(admin).Delete("/{id}", s.deleteProfile)
			})

			r.Route("/llm-configs", func(r chi.Router) {
				r.Get("/", s.listLLMConfigs)
				r.Get("/catalog", s.llmCatalog)
				r.Get("/{id}", s.getLLMConfig)
				r.With(admin).Post("/", s.createLLMConfig)
				r.With(admin).Put("/{id}", s.updateLLMConfig)
				r.With(admin).Delete("/{id}", s.deleteLLMConfig)
				r.With(admin).Post("/{id}/test", s.testLLMConfig)
			})
		})
	})
	return r
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.d.Ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.d.Ready(ctx); err != nil {
			s.log.Warn().Err(err).Msg("readiness check failed")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "not ready"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			ev := s.log.Info()
			switch {
			case status >= 500:
				ev = s.log.Error()
			case status >= 400:
				ev = s.log.Warn()
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
				ev = s.log.Debug()
			}
			ev.Str("reqId", middleware.GetReqID(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", status).
				Int("bytes", ww.BytesWritten()).
				Dur("duration", time.Since(start)).
				Str("ip", middleware.GetClientIP(r.Context())).
				Msg("http request")
		}()
		next.ServeHTTP(ww, r)
	})
}

// publish fans an event out to the org's browsers. Bus may be nil in tests.
func (s *server) publish(ctx context.Context, orgID uuid.UUID, callID *uuid.UUID, typ domain.EventType, payload any) {
	s.publishEvent(ctx, domain.Event{Type: typ, OrgID: orgID, CallID: callID, Payload: payload})
}

func (s *server) publishEvent(ctx context.Context, ev domain.Event) {
	if s.d.Bus == nil {
		return
	}
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.At.IsZero() {
		ev.At = s.now()
	}
	s.d.Bus.Publish(ctx, ev)
}

func (s *server) publishCall(ctx context.Context, typ domain.EventType, c *domain.Call) {
	id := c.ID
	snap := *c
	s.publish(ctx, c.OrgID, &id, typ, map[string]any{"call": &snap})
}

// goBackground runs fn detached from the request context.
func (s *server) goBackground(ctx context.Context, timeout time.Duration, fn func(ctx context.Context)) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error().Interface("panic", rec).Msg("background task panicked")
			}
		}()
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		fn(bctx)
	}()
}
