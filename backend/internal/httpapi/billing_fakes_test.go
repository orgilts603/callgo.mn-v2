package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing/memory"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/payments/mock"
)

var billingTestJWT = []byte("billing-test-secret")

type billingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *billingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *billingClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type billingBusRec struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *billingBusRec) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

type billingAuditRec struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *billingAuditRec) record(_ context.Context, orgID uuid.UUID, actor *uuid.UUID, action, targetType, targetID string, meta map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, domain.AuditEntry{OrgID: orgID, ActorID: actor, Action: action, TargetType: targetType, TargetID: targetID, Meta: meta})
}

func (a *billingAuditRec) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.entries {
		out = append(out, e.Action)
	}
	return out
}

type billingEnv struct {
	t     *testing.T
	clock *billingClock
	store *memory.Store
	pay   *mock.Provider
	bus   *billingBusRec
	audit *billingAuditRec
	svc   *billing.Service
	h     http.Handler

	org, org2                domain.Organization
	ownerTok, opTok, org2Tok string
	ownerID                  uuid.UUID
}

func newBillingEnv(t *testing.T, withSvc bool) *billingEnv {
	t.Helper()
	e := &billingEnv{
		t: t, clock: &billingClock{t: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)},
		store: memory.New(), pay: mock.New(), bus: &billingBusRec{}, audit: &billingAuditRec{},
	}
	e.pay.Now = e.clock.Now
	e.org = domain.Organization{ID: uuid.New(), Name: "Demo ХХК", Slug: "demo", Status: domain.OrgActive, Timezone: "Asia/Ulaanbaatar"}
	e.org2 = domain.Organization{ID: uuid.New(), Name: "Other", Slug: "other", Status: domain.OrgActive}
	e.store.PutOrg(e.org)
	e.store.PutOrg(e.org2)
	e.svc = billing.New(e.store, e.store, e.pay, e.bus, billing.Config{Now: e.clock.Now}, zerolog.Nop())

	deps := BillingDeps{Audit: e.audit.record}
	if withSvc {
		deps.Svc = e.svc
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		mountBilling(r, deps, Config{JWTSecret: billingTestJWT}, zerolog.Nop())
	})
	e.h = r

	e.ownerID = uuid.New()
	e.ownerTok = e.token(auth.Claims{UserID: e.ownerID, OrgID: e.org.ID, Role: domain.RoleOwner})
	e.opTok = e.token(auth.Claims{UserID: uuid.New(), OrgID: e.org.ID, Role: domain.RoleOperator})
	e.org2Tok = e.token(auth.Claims{UserID: uuid.New(), OrgID: e.org2.ID, Role: domain.RoleAdmin})
	return e
}

func (e *billingEnv) token(c auth.Claims) string {
	e.t.Helper()
	tok, err := auth.IssueToken(billingTestJWT, c, time.Hour)
	require.NoError(e.t, err)
	return tok
}

type billingResp struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (r billingResp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(r.Body, &m), string(r.Body))
	return m
}

func (e *billingEnv) do(method, path, tok string, body any) billingResp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(e.t, err)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return billingResp{Code: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
}
