package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type adminEnv struct {
	t        *testing.T
	id       *admIdentity
	users    *admUsers
	billing  *admBilling
	audit    *admAudit
	h        http.Handler
	now      time.Time
	orgA     domain.Organization
	orgB     domain.Organization
	staff    domain.User
	tenant   domain.User
	staffTok string
	tenTok   string
	setCalls []setSubCall
	paidNote string
}

type setSubCall struct {
	org    uuid.UUID
	plan   string
	status *domain.SubscriptionStatus
	custom *domain.Plan
	end    *time.Time
}

var adminPlans = map[string]domain.Plan{
	"trial":      {Code: "trial", MonthlyMNT: 0},
	"starter":    {Code: "starter", MonthlyMNT: 99_000},
	"growth":     {Code: "growth", MonthlyMNT: 349_000},
	"enterprise": {Code: "enterprise", MonthlyMNT: 1_500_000},
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	e := &adminEnv{t: t, id: newAdmIdentity(), billing: newAdmBilling(), audit: &admAudit{},
		now: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)}
	e.users = &admUsers{id: e.id, users: map[uuid.UUID]domain.User{}}
	e.orgA = domain.Organization{ID: uuid.New(), Name: "Alpha Bank", Slug: "alpha", PlanCode: "starter", Status: domain.OrgActive}
	e.orgB = domain.Organization{ID: uuid.New(), Name: "Beta Telecom", Slug: "beta", PlanCode: "trial", Status: domain.OrgActive}
	for _, o := range []domain.Organization{e.orgA, e.orgB} {
		c := o
		e.id.orgs[o.ID] = &c
	}
	e.staff = domain.User{ID: uuid.New(), OrgID: e.orgA.ID, Email: "staff@callgo.mn", Role: domain.RoleOwner, IsPlatformAdmin: true}
	e.tenant = domain.User{ID: uuid.New(), OrgID: e.orgA.ID, Email: "boss@alpha.mn", Role: domain.RoleOwner}
	e.users.users[e.staff.ID] = e.staff
	e.users.users[e.tenant.ID] = e.tenant
	e.id.counts[e.orgA.ID], e.id.counts[e.orgB.ID] = 2, 1
	e.staffTok = e.token(e.staff)
	e.tenTok = e.token(e.tenant)

	d := AdminDeps{
		Identity: e.id, Billing: e.billing, Users: e.users, Audit: e.audit,
		Now:   func() time.Time { return e.now },
		Plans: func(code string) (domain.Plan, bool) { p, ok := adminPlans[code]; return p, ok },
		SetSubscription: func(_ context.Context, orgID uuid.UUID, plan string, st *domain.SubscriptionStatus, custom *domain.Plan, end *time.Time) (*domain.Subscription, error) {
			e.setCalls = append(e.setCalls, setSubCall{orgID, plan, st, custom, end})
			s := &domain.Subscription{ID: uuid.New(), OrgID: orgID, PlanCode: plan, Status: domain.SubActive, CustomLimits: custom}
			if st != nil {
				s.Status = *st
			}
			if end != nil {
				s.CurrentPeriodEnd = *end
			}
			return s, nil
		},
		MarkInvoicePaid: func(_ context.Context, id uuid.UUID, note string) (*domain.Invoice, error) {
			for _, list := range e.billing.invoices {
				for _, inv := range list {
					if inv.ID == id {
						e.paidNote = note
						inv.Status = domain.InvoicePaid
						return &inv, nil
					}
				}
			}
			return nil, domain.ErrNotFound
		},
	}
	r := chi.NewRouter()
	mountAdmin(r, d, Config{JWTSecret: testJWT}, zerolog.Nop())
	e.h = r
	return e
}

func (e *adminEnv) token(u domain.User) string {
	tok, err := auth.IssueToken(testJWT, auth.Claims{UserID: u.ID, OrgID: u.OrgID, Role: u.Role}, time.Hour)
	require.NoError(e.t, err)
	return tok
}

func (e *adminEnv) do(method, path, tok string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(e.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

func TestAdminGuard(t *testing.T) {
	e := newAdminEnv(t)
	paths := []struct{ method, path string }{
		{"GET", "/api/admin/stats"},
		{"GET", "/api/admin/orgs"},
		{"GET", "/api/admin/orgs/" + e.orgA.ID.String()},
		{"PUT", "/api/admin/orgs/" + e.orgA.ID.String()},
		{"PUT", "/api/admin/orgs/" + e.orgA.ID.String() + "/subscription"},
		{"POST", "/api/admin/invoices/" + uuid.NewString() + "/mark-paid"},
	}
	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			require.Equal(t, http.StatusUnauthorized, e.do(p.method, p.path, "", nil).Code)
			require.Equal(t, http.StatusForbidden, e.do(p.method, p.path, e.tenTok, map[string]any{}).Code)
		})
	}
	// A token for a user that no longer exists is forbidden, not a 500.
	ghost := e.token(domain.User{ID: uuid.New(), OrgID: e.orgA.ID, Role: domain.RoleOwner})
	require.Equal(t, http.StatusForbidden, e.do("GET", "/api/admin/stats", ghost, nil).Code)
	// A disabled platform admin is refused.
	off := e.staff
	off.Status = domain.UserDisabled
	e.users.users[off.ID] = off
	require.Equal(t, http.StatusForbidden, e.do("GET", "/api/admin/stats", e.staffTok, nil).Code)
}

func TestAdminListOrgs(t *testing.T) {
	e := newAdminEnv(t)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	e.billing.subs[e.orgA.ID] = &domain.Subscription{OrgID: e.orgA.ID, PlanCode: "starter", Status: domain.SubActive,
		CurrentPeriodStart: start, CurrentPeriodEnd: start.AddDate(0, 1, 0)}
	e.billing.usage[e.orgA.ID] = domain.UsageSummary{Calls: 7, Minutes: 12.5}

	rec := e.do("GET", "/api/admin/orgs", e.staffTok, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeBody[struct {
		Items []adminOrgRow `json:"items"`
		Total int           `json:"total"`
	}](t, rec)
	require.Equal(t, 2, resp.Total)
	require.Len(t, resp.Items, 2)
	a, b := resp.Items[0], resp.Items[1]
	require.Equal(t, "Alpha Bank", a.Org.Name)
	require.NotNil(t, a.Subscription)
	require.Equal(t, 7, a.Usage.Calls)
	require.Equal(t, 2, a.Users)
	require.Nil(t, b.Subscription, "org without subscription lists with null subscription")
	require.Equal(t, 1, b.Users)
	// Usage window follows the subscription period; without one, the month.
	require.Equal(t, start, e.billing.windows[0][0])
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), e.billing.windows[1][0])
	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), e.billing.windows[1][1])

	rec = e.do("GET", "/api/admin/orgs?q=BETA&limit=1", e.staffTok, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	resp = decodeBody[struct {
		Items []adminOrgRow `json:"items"`
		Total int           `json:"total"`
	}](t, rec)
	require.Len(t, resp.Items, 1)
	require.Equal(t, "Beta Telecom", resp.Items[0].Org.Name)

	require.Equal(t, http.StatusBadRequest, e.do("GET", "/api/admin/orgs?limit=0", e.staffTok, nil).Code)
	require.Equal(t, http.StatusBadRequest, e.do("GET", "/api/admin/orgs?offset=-1", e.staffTok, nil).Code)
}

func TestAdminGetOrg(t *testing.T) {
	e := newAdminEnv(t)
	custom := domain.Plan{Code: "enterprise", MonthlyMNT: 2_000_000, MaxUsers: 99}
	e.billing.subs[e.orgA.ID] = &domain.Subscription{OrgID: e.orgA.ID, PlanCode: "enterprise", Status: domain.SubActive, CustomLimits: &custom,
		CurrentPeriodStart: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), CurrentPeriodEnd: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	e.billing.invoices[e.orgA.ID] = []domain.Invoice{{ID: uuid.New(), OrgID: e.orgA.ID, Number: "CG-2026-000001", TotalMNT: 110}}

	rec := e.do("GET", "/api/admin/orgs/"+e.orgA.ID.String(), e.staffTok, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeBody[struct {
		Org      domain.Organization  `json:"org"`
		Sub      *domain.Subscription `json:"subscription"`
		Plan     *domain.Plan         `json:"plan"`
		Users    []domain.User        `json:"users"`
		Invoices []domain.Invoice     `json:"invoices"`
	}](t, rec)
	require.Equal(t, e.orgA.ID, resp.Org.ID)
	require.NotNil(t, resp.Sub)
	require.NotNil(t, resp.Plan)
	require.Equal(t, 99, resp.Plan.MaxUsers, "custom limits override the plan")
	require.Len(t, resp.Users, 2)
	require.Len(t, resp.Invoices, 1)

	// Org without subscription: plan falls back to Organization.PlanCode.
	rec = e.do("GET", "/api/admin/orgs/"+e.orgB.ID.String(), e.staffTok, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.JSONEq(t, "null", string(raw["subscription"]))
	require.JSONEq(t, `[]`, string(raw["invoices"]))
	require.Contains(t, string(raw["plan"]), `"trial"`)

	require.Equal(t, http.StatusNotFound, e.do("GET", "/api/admin/orgs/"+uuid.NewString(), e.staffTok, nil).Code)
	require.Equal(t, http.StatusBadRequest, e.do("GET", "/api/admin/orgs/nope", e.staffTok, nil).Code)
}

func TestAdminUpdateOrgStatus(t *testing.T) {
	e := newAdminEnv(t)
	path := "/api/admin/orgs/" + e.orgA.ID.String()

	rec := e.do("PUT", path, e.staffTok, map[string]any{"status": "suspended"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeBody[struct {
		Org domain.Organization `json:"org"`
	}](t, rec)
	require.Equal(t, domain.OrgSuspended, resp.Org.Status)
	require.Equal(t, domain.OrgSuspended, e.id.orgs[e.orgA.ID].Status)

	require.Len(t, e.audit.entries, 1)
	got := e.audit.entries[0]
	require.Equal(t, "admin.org.status", got.Action)
	require.Equal(t, e.orgA.ID, got.OrgID)
	require.Equal(t, "staff@callgo.mn", got.ActorEmail)
	require.Equal(t, "active", got.Meta["from"])
	require.Equal(t, "suspended", got.Meta["to"])

	require.Equal(t, http.StatusBadRequest, e.do("PUT", path, e.staffTok, map[string]any{"status": "frozen"}).Code)
	require.Equal(t, http.StatusBadRequest, e.do("PUT", path, e.staffTok, map[string]any{}).Code)
	require.Equal(t, http.StatusNotFound, e.do("PUT", "/api/admin/orgs/"+uuid.NewString(), e.staffTok, map[string]any{"status": "active"}).Code)
	require.Len(t, e.audit.entries, 1, "rejected requests are not audited")
}

func TestAdminSetSubscription(t *testing.T) {
	e := newAdminEnv(t)
	path := "/api/admin/orgs/" + e.orgB.ID.String() + "/subscription"
	end := e.now.Add(30 * 24 * time.Hour)

	rec := e.do("PUT", path, e.staffTok, map[string]any{
		"planCode": "enterprise", "status": "active",
		"customLimits":     map[string]any{"code": "enterprise", "maxUsers": 50},
		"currentPeriodEnd": end.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeBody[struct {
		Sub domain.Subscription `json:"subscription"`
	}](t, rec)
	require.Equal(t, "enterprise", resp.Sub.PlanCode)
	require.Equal(t, domain.SubActive, resp.Sub.Status)
	require.Len(t, e.setCalls, 1)
	c := e.setCalls[0]
	require.Equal(t, e.orgB.ID, c.org)
	require.NotNil(t, c.status)
	require.Equal(t, domain.SubActive, *c.status)
	require.NotNil(t, c.custom)
	require.Equal(t, 50, c.custom.MaxUsers)
	require.NotNil(t, c.end)
	require.True(t, c.end.Equal(end))
	require.Equal(t, "admin.subscription.set", e.audit.entries[0].Action)

	// Only the plan: status/custom/end stay nil.
	rec = e.do("PUT", path, e.staffTok, map[string]any{"planCode": "growth"})
	require.Equal(t, http.StatusOK, rec.Code)
	c = e.setCalls[1]
	require.Nil(t, c.status)
	require.Nil(t, c.custom)
	require.Nil(t, c.end)

	bad := []map[string]any{
		{},
		{"planCode": "platinum"},
		{"planCode": "growth", "status": "weird"},
		{"planCode": "growth", "currentPeriodEnd": e.now.Add(-time.Hour).Format(time.RFC3339)},
	}
	for i, body := range bad {
		require.Equal(t, http.StatusBadRequest, e.do("PUT", path, e.staffTok, body).Code, fmt.Sprintf("case %d", i))
	}
	require.Len(t, e.setCalls, 2)
	require.Equal(t, http.StatusNotFound,
		e.do("PUT", "/api/admin/orgs/"+uuid.NewString()+"/subscription", e.staffTok, map[string]any{"planCode": "growth"}).Code)
}

func TestAdminMarkInvoicePaid(t *testing.T) {
	e := newAdminEnv(t)
	inv := domain.Invoice{ID: uuid.New(), OrgID: e.orgA.ID, Number: "CG-2026-000042", Status: domain.InvoiceOpen}
	e.billing.invoices[e.orgA.ID] = []domain.Invoice{inv}

	rec := e.do("POST", "/api/admin/invoices/"+inv.ID.String()+"/mark-paid", e.staffTok, map[string]any{"note": " TDB transfer 123 "})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeBody[struct {
		Invoice domain.Invoice `json:"invoice"`
	}](t, rec)
	require.Equal(t, domain.InvoicePaid, resp.Invoice.Status)
	require.Equal(t, "TDB transfer 123", e.paidNote)
	require.Equal(t, "admin.invoice.mark_paid", e.audit.entries[0].Action)
	require.Equal(t, e.orgA.ID, e.audit.entries[0].OrgID, "audit is attributed to the invoice's org")

	// The note is optional.
	require.Equal(t, http.StatusOK, e.do("POST", "/api/admin/invoices/"+inv.ID.String()+"/mark-paid", e.staffTok, nil).Code)
	require.Equal(t, http.StatusNotFound, e.do("POST", "/api/admin/invoices/"+uuid.NewString()+"/mark-paid", e.staffTok, map[string]any{}).Code)
	require.Equal(t, http.StatusBadRequest, e.do("POST", "/api/admin/invoices/x/mark-paid", e.staffTok, map[string]any{}).Code)
}

func TestAdminStats(t *testing.T) {
	e := newAdminEnv(t)
	e.billing.subs[e.orgA.ID] = &domain.Subscription{OrgID: e.orgA.ID, PlanCode: "growth", Status: domain.SubActive}
	e.billing.subs[e.orgB.ID] = &domain.Subscription{OrgID: e.orgB.ID, PlanCode: "starter", Status: domain.SubTrialing}
	// A third org with a custom-priced active subscription and a canceled one.
	orgC := domain.Organization{ID: uuid.New(), Name: "Gamma", Slug: "gamma"}
	orgD := domain.Organization{ID: uuid.New(), Name: "Delta", Slug: "delta"}
	for _, o := range []domain.Organization{orgC, orgD} {
		c := o
		e.id.orgs[o.ID] = &c
	}
	e.billing.subs[orgC.ID] = &domain.Subscription{OrgID: orgC.ID, PlanCode: "enterprise", Status: domain.SubActive,
		CustomLimits: &domain.Plan{Code: "enterprise", MonthlyMNT: 2_000_000}}
	e.billing.subs[orgD.ID] = &domain.Subscription{OrgID: orgD.ID, PlanCode: "starter", Status: domain.SubCanceled}
	e.billing.usage[e.orgA.ID] = domain.UsageSummary{Calls: 10, Minutes: 30.5}
	e.billing.usage[orgC.ID] = domain.UsageSummary{Calls: 5, Minutes: 4}

	rec := e.do("GET", "/api/admin/stats", e.staffTok, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeBody[struct {
		Orgs                int     `json:"orgs"`
		ActiveSubscriptions int     `json:"activeSubscriptions"`
		MRR                 int64   `json:"mrrMnt"`
		CallsToday          int     `json:"callsToday"`
		MinutesToday        float64 `json:"minutesToday"`
	}](t, rec)
	require.Equal(t, 4, resp.Orgs)
	require.Equal(t, 2, resp.ActiveSubscriptions, "trialing and canceled are not active")
	require.Equal(t, int64(349_000+2_000_000), resp.MRR, "trialing counts 0; custom price overrides the plan")
	require.Equal(t, 15, resp.CallsToday)
	require.InDelta(t, 34.5, resp.MinutesToday, 1e-9)

	// "Today" is the Ulaanbaatar calendar day (UTC+8): 2026-09-30T10:00Z is
	// already 18:00 on the 30th there.
	for _, w := range e.billing.windows {
		require.Equal(t, 24*time.Hour, w[1].Sub(w[0]))
		require.Equal(t, w[0].UTC(), time.Date(2026, 9, 29, 16, 0, 0, 0, time.UTC))
	}
}

func TestAdminStatsPagesThroughAllOrgs(t *testing.T) {
	e := newAdminEnv(t)
	for i := 0; i < adminStatsPage+50; i++ {
		id := uuid.New()
		e.id.orgs[id] = &domain.Organization{ID: id, Name: fmt.Sprintf("Org %04d", i), Slug: fmt.Sprintf("org-%d", i)}
		e.billing.usage[id] = domain.UsageSummary{Calls: 1, Minutes: 1}
	}
	rec := e.do("GET", "/api/admin/stats", e.staffTok, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	resp := decodeBody[struct {
		Orgs       int `json:"orgs"`
		CallsToday int `json:"callsToday"`
	}](t, rec)
	require.Equal(t, adminStatsPage+52, resp.Orgs)
	require.Equal(t, adminStatsPage+50, resp.CallsToday)
	require.Len(t, e.id.pageCalls, 2)
}
