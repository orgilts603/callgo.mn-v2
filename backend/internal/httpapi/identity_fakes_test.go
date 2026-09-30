package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/identity"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/identity/identitytest"
	cgmw "github.com/orgilts603/callgo.mn-v2/backend/internal/middleware"
)

var identityTestJWT = []byte("identity-api-test-secret")

type idnMail struct{ to, text string }

type idnMailer struct {
	mu   sync.Mutex
	sent []idnMail
}

func (m *idnMailer) Send(_ context.Context, to, _, text, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, idnMail{to, text})
	return nil
}

var idnTokenRe = regexp.MustCompile(`(/[a-z-]+)\?token=([^\s"<]+)`)

func (m *idnMailer) token(t *testing.T, to, path string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if m.sent[i].to != to {
			continue
		}
		for _, match := range idnTokenRe.FindAllStringSubmatch(m.sent[i].text, -1) {
			if match[1] == path {
				tok, err := url.QueryUnescape(match[2])
				require.NoError(t, err)
				return tok
			}
		}
	}
	t.Fatalf("no %s mail to %s", path, to)
	return ""
}

func (m *idnMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type idnSubs struct {
	mu   sync.Mutex
	subs map[uuid.UUID]*domain.Subscription
}

func (s *idnSubs) StartTrial(_ context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	end := now.Add(14 * 24 * time.Hour)
	sub := &domain.Subscription{ID: uuid.New(), OrgID: orgID, PlanCode: "trial", Status: domain.SubTrialing,
		CurrentPeriodStart: now, CurrentPeriodEnd: end, TrialEndsAt: &end, CreatedAt: now, UpdatedAt: now}
	s.subs[orgID] = sub
	return sub, nil
}

func (s *idnSubs) get(_ context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub, ok := s.subs[orgID]; ok {
		return sub, nil
	}
	return nil, domain.ErrNotFound
}

type identityEnv struct {
	t        *testing.T
	repo     *identitytest.Memory
	mail     *idnMailer
	svc      *identity.Service
	h        http.Handler
	maxUsers int
	features map[string]bool
}

type identityEnvOpts struct {
	allowSignup bool
	rateLimit   bool // use the real default limiter instead of a no-op
}

// newIdentityEnv mounts the identity routes on a fresh router under /api,
// plus stand-in /api/calls routes behind auth + OrgGate (for gate/scope tests).
func newIdentityEnv(t *testing.T, o identityEnvOpts) *identityEnv {
	t.Helper()
	e := &identityEnv{t: t, repo: identitytest.NewMemory(), mail: &idnMailer{}, maxUsers: 3, features: map[string]bool{"api": true}}
	subs := &idnSubs{subs: map[uuid.UUID]*domain.Subscription{}}
	e.svc = identity.New(e.repo, e.repo, e.mail, subs, identity.Config{
		AppURL: "https://app.callgo.mn", AllowSignup: o.allowSignup, JWTSecret: identityTestJWT,
	}, zerolog.Nop())
	e.svc.Subscription = subs.get
	e.svc.Limits = func(context.Context, uuid.UUID) (domain.Plan, error) {
		return domain.Plan{Code: "trial", MaxUsers: e.maxUsers, Features: []string{"api"}}, nil
	}
	hasFeature := func(_ context.Context, _ uuid.UUID, f string) (bool, error) { return e.features[f], nil }
	e.svc.HasFeature = hasFeature

	deps := IdentityDeps{Svc: e.svc, HasFeature: hasFeature}
	if !o.rateLimit {
		deps.AuthRateLimit = func(next http.Handler) http.Handler { return next }
	}
	orgStatus := func(ctx context.Context, id uuid.UUID) (domain.OrgStatus, error) {
		org, err := e.repo.GetOrg(ctx, id)
		if err != nil {
			return "", err
		}
		return org.Status, nil
	}
	ok := func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		mountIdentity(r, deps, Config{JWTSecret: identityTestJWT}, zerolog.Nop())
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAuth(identityTestJWT, auth.WithAPIKeys(e.svc)), cgmw.OrgGate(orgStatus))
			r.With(auth.RequireScope("calls:read")).Get("/calls", ok)
			r.With(auth.RequireScope("calls:write")).Post("/calls/dial", ok)
		})
	})
	e.h = r
	return e
}

func (e *identityEnv) do(method, path, token string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(e.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:4444"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

type idnAuthResp struct {
	Token        string               `json:"token"`
	RefreshToken string               `json:"refreshToken"`
	User         domain.User          `json:"user"`
	Org          domain.Organization  `json:"org"`
	Subscription *domain.Subscription `json:"subscription"`
}

// signup creates an org + owner through the service (signup must be allowed
// on the env) and returns the auth response.
func (e *identityEnv) signup(org, email string) idnAuthResp {
	e.t.Helper()
	w := e.do(http.MethodPost, "/api/auth/signup", "", map[string]any{"orgName": org, "email": email, "password": "password1", "name": "Owner"})
	require.Equal(e.t, http.StatusCreated, w.Code, w.Body.String())
	return decode[idnAuthResp](e.t, w)
}

// member invites and accepts email with role in owner's org.
func (e *identityEnv) member(owner idnAuthResp, email string, role domain.Role) idnAuthResp {
	e.t.Helper()
	prev := e.maxUsers
	e.maxUsers = 0
	defer func() { e.maxUsers = prev }()
	w := e.do(http.MethodPost, "/api/org/invitations", owner.Token, map[string]any{"email": email, "role": role})
	require.Equal(e.t, http.StatusCreated, w.Code, w.Body.String())
	w = e.do(http.MethodPost, "/api/auth/accept-invitation", "", map[string]any{"token": e.mail.token(e.t, email, "/accept-invitation"), "name": "Member", "password": "password1"})
	require.Equal(e.t, http.StatusOK, w.Code, w.Body.String())
	return decode[idnAuthResp](e.t, w)
}
