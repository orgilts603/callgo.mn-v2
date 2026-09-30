package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/sms"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks/webhookstest"
)

type integFakeOrgs struct {
	mu   sync.Mutex
	orgs map[uuid.UUID]*domain.Organization
}

func (f *integFakeOrgs) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orgs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *o
	c.Settings = map[string]any{}
	for k, v := range o.Settings {
		c.Settings[k] = v
	}
	return &c, nil
}

func (f *integFakeOrgs) UpdateOrg(_ context.Context, o *domain.Organization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *o
	f.orgs[o.ID] = &c
	return nil
}

type integFakeAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (f *integFakeAudit) Append(_ context.Context, e *domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, *e)
	return nil
}

func (f *integFakeAudit) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e.Action)
	}
	return out
}

type integFakeCalls struct {
	domain.CallRepository
	calls map[uuid.UUID]*domain.Call
}

func (f integFakeCalls) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	if c, ok := f.calls[id]; ok {
		return c, nil
	}
	return nil, domain.ErrNotFound
}

type integFakeBus struct{}

func (integFakeBus) Publish(context.Context, domain.Event) {}

var integJWT = []byte("integrations-test-secret")

// integEnv is a router with mountIntegrations behind auth.RequireAuth.
type integEnv struct {
	t        *testing.T
	repo     *webhookstest.MemRepo
	orgs     *integFakeOrgs
	audit    *integFakeAudit
	disp     *webhooks.Dispatcher
	mock     *sms.Mock
	features map[string]bool
	h        http.Handler

	org, otherOrg           domain.Organization
	adminTok, opTok, othTok string
	call                    domain.Call
	foreignCall             domain.Call
}

func newIntegEnv(t *testing.T) *integEnv {
	t.Helper()
	e := &integEnv{t: t, repo: webhookstest.New(), audit: &integFakeAudit{}, mock: sms.NewMock(),
		features: map[string]bool{"webhooks": true, "sms": true}}
	e.org = domain.Organization{ID: uuid.New(), Name: "Acme", Settings: map[string]any{"recordCalls": true}}
	e.otherOrg = domain.Organization{ID: uuid.New(), Name: "Other"}
	e.orgs = &integFakeOrgs{orgs: map[uuid.UUID]*domain.Organization{e.org.ID: &e.org, e.otherOrg.ID: &e.otherOrg}}
	e.call = domain.Call{ID: uuid.New(), OrgID: e.org.ID}
	e.foreignCall = domain.Call{ID: uuid.New(), OrgID: e.otherOrg.ID}

	e.disp = webhooks.New(e.repo, nil, webhooks.Config{}, integFakeBus{}, zerolog.Nop())
	xor := func(b []byte) ([]byte, error) {
		out := make([]byte, len(b))
		for i := range b {
			out[i] = b[i] ^ 0x21
		}
		return out, nil
	}
	svc := sms.New(e.repo,
		sms.SettingsLoaderFunc(func(ctx context.Context, id uuid.UUID) (map[string]any, error) {
			o, err := e.orgs.GetOrg(ctx, id)
			if err != nil {
				return nil, err
			}
			return o.Settings, nil
		}),
		sms.DefaultFactory(xor, sms.WithMock(e.mock)), nil, nil, zerolog.Nop(),
		sms.WithCrypto(sms.Crypto{Encrypt: xor, Decrypt: xor}))

	deps := IntegrationsDeps{
		Repo: e.repo, Webhooks: e.disp, SMS: svc, Orgs: e.orgs,
		Calls:      integFakeCalls{calls: map[uuid.UUID]*domain.Call{e.call.ID: &e.call, e.foreignCall.ID: &e.foreignCall}},
		HasFeature: func(_ context.Context, _ uuid.UUID, f string) (bool, error) { return e.features[f], nil },
		Audit:      e.audit,
	}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(integJWT))
		mountIntegrations(r, deps, Config{JWTSecret: integJWT}, zerolog.Nop())
	})
	e.h = r

	tok := func(org uuid.UUID, role domain.Role) string {
		s, err := auth.IssueToken(integJWT, auth.Claims{UserID: uuid.New(), OrgID: org, Role: role}, time.Hour)
		require.NoError(t, err)
		return s
	}
	e.adminTok = tok(e.org.ID, domain.RoleAdmin)
	e.opTok = tok(e.org.ID, domain.RoleOperator)
	e.othTok = tok(e.otherOrg.ID, domain.RoleOwner)
	return e
}

func (e *integEnv) do(method, path, token string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	return integDo(e.t, e.h, method, path, token, body)
}

func (e *integEnv) seedHook(org uuid.UUID, url string, events ...string) domain.Webhook {
	e.t.Helper()
	w := domain.Webhook{ID: uuid.New(), OrgID: org, URL: url, Secret: "whsec_seed", SecretHint: "whsec_…seed", Events: events, Active: true}
	require.NoError(e.t, e.repo.CreateWebhook(context.Background(), &w))
	return w
}
