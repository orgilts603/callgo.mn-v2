package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func billingErrCode(t *testing.T, r billingResp) string {
	t.Helper()
	m := r.json(t)
	e, _ := m["error"].(map[string]any)
	require.NotNil(t, e, string(r.Body))
	return e["code"].(string)
}

func TestBillingPlansPublic(t *testing.T) {
	e := newBillingEnv(t, true)
	r := e.do(http.MethodGet, "/api/billing/plans", "", nil)
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	items := r.json(t)["items"].([]any)
	require.Len(t, items, 3)
	first := items[0].(map[string]any)
	assert.Equal(t, "starter", first["code"])
	assert.Equal(t, float64(290000), first["monthlyMnt"])
	assert.Equal(t, float64(3), r.json(t)["total"])
}

func TestBillingRequiresAuth(t *testing.T) {
	e := newBillingEnv(t, true)
	for _, p := range []string{"/api/billing/subscription", "/api/billing/usage", "/api/billing/invoices", "/api/billing/payments/" + uuid.NewString()} {
		r := e.do(http.MethodGet, p, "", nil)
		assert.Equal(t, http.StatusUnauthorized, r.Code, p)
	}
}

func TestBillingNotConfigured(t *testing.T) {
	e := newBillingEnv(t, false)
	r := e.do(http.MethodGet, "/api/billing/subscription", e.ownerTok, nil)
	assert.Equal(t, http.StatusInternalServerError, r.Code)
	assert.Contains(t, string(r.Body), "billing not configured")
	r = e.do(http.MethodGet, "/api/billing/plans", "", nil)
	assert.Equal(t, http.StatusOK, r.Code, "plans are static")
}

func TestBillingSubscriptionFlow(t *testing.T) {
	e := newBillingEnv(t, true)

	r := e.do(http.MethodGet, "/api/billing/subscription", e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	m := r.json(t)
	assert.Equal(t, "trial", m["subscription"].(map[string]any)["planCode"])
	assert.Equal(t, "trialing", m["subscription"].(map[string]any)["status"])
	assert.Equal(t, "trial", m["plan"].(map[string]any)["code"])
	assert.Equal(t, float64(100), m["limits"].(map[string]any)["includedMinutes"])
	assert.Equal(t, float64(100), m["usage"].(map[string]any)["includedMinutes"])

	// Operators cannot change the plan.
	r = e.do(http.MethodPost, "/api/billing/subscription", e.opTok, map[string]any{"planCode": "starter"})
	assert.Equal(t, http.StatusForbidden, r.Code)

	r = e.do(http.MethodPost, "/api/billing/subscription", e.ownerTok, map[string]any{"planCode": "enterprise"})
	assert.Equal(t, http.StatusBadRequest, r.Code)
	assert.Equal(t, "invalid", billingErrCode(t, r))
	assert.Contains(t, string(r.Body), "not available")
	r = e.do(http.MethodPost, "/api/billing/subscription", e.ownerTok, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, r.Code)

	r = e.do(http.MethodPost, "/api/billing/subscription", e.ownerTok, map[string]any{"planCode": "starter"})
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	m = r.json(t)
	sub := m["subscription"].(map[string]any)
	assert.Equal(t, "starter", sub["planCode"])
	assert.Equal(t, "active", sub["status"])
	inv := m["invoice"].(map[string]any)
	assert.Equal(t, "open", inv["status"])
	assert.Equal(t, float64(319000), inv["totalMnt"])
	invID := inv["id"].(string)

	r = e.do(http.MethodPost, "/api/billing/subscription", e.ownerTok, map[string]any{"planCode": "starter"})
	assert.Equal(t, http.StatusConflict, r.Code)

	// Invoices.
	r = e.do(http.MethodGet, "/api/billing/invoices", e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Len(t, r.json(t)["items"].([]any), 1)
	r = e.do(http.MethodGet, "/api/billing/invoices/"+invID, e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Empty(t, r.json(t)["payments"].([]any))
	r = e.do(http.MethodGet, "/api/billing/invoices/"+invID, e.org2Tok, nil)
	assert.Equal(t, http.StatusNotFound, r.Code, "other org cannot read the invoice")
	r = e.do(http.MethodGet, "/api/billing/invoices/nope", e.opTok, nil)
	assert.Equal(t, http.StatusBadRequest, r.Code)

	// Printable document.
	r = e.do(http.MethodGet, "/api/billing/invoices/"+invID+"/pdf", e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Equal(t, "text/html; charset=utf-8", r.Header.Get("Content-Type"))
	assert.True(t, strings.HasPrefix(r.Header.Get("Content-Disposition"), "inline"))
	assert.Contains(t, string(r.Body), "НЭХЭМЖЛЭХ")
	assert.Contains(t, string(r.Body), "Demo ХХК")

	// Pay with the mock provider.
	r = e.do(http.MethodPost, "/api/billing/invoices/"+invID+"/pay", e.opTok, map[string]any{"provider": "mock"})
	assert.Equal(t, http.StatusForbidden, r.Code)
	r = e.do(http.MethodPost, "/api/billing/invoices/"+invID+"/pay", e.ownerTok, map[string]any{"provider": "stripe"})
	assert.Equal(t, http.StatusBadRequest, r.Code)
	r = e.do(http.MethodPost, "/api/billing/invoices/"+invID+"/pay", e.ownerTok, map[string]any{"provider": "mock"})
	require.Equal(t, http.StatusCreated, r.Code, string(r.Body))
	pay := r.json(t)["payment"].(map[string]any)
	payID := pay["id"].(string)
	assert.Equal(t, "MOCK:"+payID, pay["qrText"])
	assert.Equal(t, "pending", pay["status"])
	assert.NotEmpty(t, pay["deepLinks"])
	assert.NotEmpty(t, pay["expiresAt"])

	r = e.do(http.MethodGet, "/api/billing/payments/"+payID, e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Equal(t, "pending", r.json(t)["payment"].(map[string]any)["status"])
	r = e.do(http.MethodGet, "/api/billing/payments/"+payID, e.org2Tok, nil)
	assert.Equal(t, http.StatusNotFound, r.Code)

	// Customer pays; the UI poll (> 10 s after creation) settles it lazily.
	require.NoError(t, e.pay.MarkPaid(payID))
	e.clock.Advance(11 * time.Second)
	r = e.do(http.MethodGet, "/api/billing/payments/"+payID, e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Equal(t, "paid", r.json(t)["payment"].(map[string]any)["status"])

	r = e.do(http.MethodGet, "/api/billing/invoices/"+invID, e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Equal(t, "paid", r.json(t)["invoice"].(map[string]any)["status"])
	r = e.do(http.MethodPost, "/api/billing/invoices/"+invID+"/pay", e.ownerTok, map[string]any{"provider": "mock"})
	assert.Equal(t, http.StatusConflict, r.Code)

	// Cancel at period end.
	r = e.do(http.MethodPost, "/api/billing/subscription/cancel", e.opTok, nil)
	assert.Equal(t, http.StatusForbidden, r.Code)
	r = e.do(http.MethodPost, "/api/billing/subscription/cancel", e.ownerTok, nil)
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	sub = r.json(t)["subscription"].(map[string]any)
	assert.NotEmpty(t, sub["canceledAt"])
	assert.Equal(t, "active", sub["status"])

	assert.Equal(t, []string{"subscription.update", "invoice.pay", "subscription.cancel"}, e.audit.actions())
	e.audit.mu.Lock()
	first := e.audit.entries[0]
	e.audit.mu.Unlock()
	require.NotNil(t, first.ActorID)
	assert.Equal(t, e.ownerID, *first.ActorID)
	assert.Equal(t, e.org.ID, first.OrgID)
	assert.Equal(t, "subscription", first.TargetType)
}

func TestBillingWebhook(t *testing.T) {
	e := newBillingEnv(t, true)
	r := e.do(http.MethodPost, "/api/billing/subscription", e.ownerTok, map[string]any{"planCode": "growth"})
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	invID := r.json(t)["invoice"].(map[string]any)["id"].(string)
	r = e.do(http.MethodPost, "/api/billing/invoices/"+invID+"/pay", e.ownerTok, nil) // default provider
	require.Equal(t, http.StatusCreated, r.Code, string(r.Body))
	payID := r.json(t)["payment"].(map[string]any)["id"].(string)

	// Callback before the provider confirms: acknowledged, nothing paid.
	r = e.do(http.MethodPost, "/api/billing/webhooks/mock?payment_id="+payID, "", nil)
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	assert.Equal(t, "SUCCESS", string(r.Body))
	p, err := e.store.GetPayment(t.Context(), uuid.MustParse(payID))
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, p.Status)

	require.NoError(t, e.pay.MarkPaid(payID))
	r = e.do(http.MethodPost, "/api/billing/webhooks/mock?payment_id="+payID, "", nil)
	require.Equal(t, http.StatusOK, r.Code)
	assert.Equal(t, "SUCCESS", string(r.Body))
	p, err = e.store.GetPayment(t.Context(), uuid.MustParse(payID))
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPaid, p.Status)
	inv, err := e.store.GetInvoice(t.Context(), uuid.MustParse(invID))
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePaid, inv.Status)

	// Repeated callback: still SUCCESS, audited once.
	r = e.do(http.MethodGet, "/api/billing/webhooks/mock?payment_id="+payID, "", nil)
	require.Equal(t, http.StatusOK, r.Code)
	n := 0
	for _, a := range e.audit.actions() {
		if a == "payment.paid" {
			n++
		}
	}
	assert.Equal(t, 1, n)

	r = e.do(http.MethodPost, "/api/billing/webhooks/mock", "", nil)
	assert.Equal(t, http.StatusBadRequest, r.Code)
	r = e.do(http.MethodPost, "/api/billing/webhooks/mock?payment_id="+uuid.NewString(), "", nil)
	assert.Equal(t, http.StatusNotFound, r.Code)
	r = e.do(http.MethodPost, "/api/billing/webhooks/qpay?payment_id="+payID, "", nil)
	assert.Equal(t, http.StatusBadRequest, r.Code, "qpay not configured in this env")

	var sawBilling bool
	e.bus.mu.Lock()
	for _, ev := range e.bus.events {
		if ev.Type == "billing.updated" {
			sawBilling = true
		}
	}
	e.bus.mu.Unlock()
	assert.True(t, sawBilling)
}

func TestBillingUsage(t *testing.T) {
	e := newBillingEnv(t, true)
	now := e.clock.Now()
	ans := now.Add(-3 * time.Minute)
	require.NoError(t, e.svc.RecordCall(t.Context(), &domain.Call{ID: uuid.New(), OrgID: e.org.ID, AnsweredAt: &ans, EndedAt: &now, DurationSec: 170}))

	r := e.do(http.MethodGet, "/api/billing/usage", e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	m := r.json(t)
	assert.Equal(t, float64(3), m["summary"].(map[string]any)["minutes"])
	assert.Len(t, m["daily"].([]any), 15) // 14-day trial spans 15 local days

	r = e.do(http.MethodGet, "/api/billing/usage?from=2026-09-01&to=2026-09-03", e.opTok, nil)
	require.Equal(t, http.StatusOK, r.Code, string(r.Body))
	daily := r.json(t)["daily"].([]any)
	require.Len(t, daily, 3) // local days 09-01 (from 08:00), 09-02, 09-03 (to 08:00)
	assert.Equal(t, "2026-09-01", daily[0].(map[string]any)["day"])
	assert.Equal(t, float64(3), daily[0].(map[string]any)["minutes"])
	assert.Equal(t, float64(1), daily[0].(map[string]any)["calls"])

	r = e.do(http.MethodGet, "/api/billing/usage?from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z", e.opTok, nil)
	assert.Equal(t, http.StatusOK, r.Code)
	r = e.do(http.MethodGet, "/api/billing/usage?from=yesterday", e.opTok, nil)
	assert.Equal(t, http.StatusBadRequest, r.Code)
	r = e.do(http.MethodGet, "/api/billing/usage?from=2026-09-05&to=2026-09-01", e.opTok, nil)
	assert.Equal(t, http.StatusBadRequest, r.Code)
}
