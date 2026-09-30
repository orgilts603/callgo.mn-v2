package qpay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/payments/qpay"
)

// fakeQPay emulates the QPay v2 merchant API.
type fakeQPay struct {
	t  *testing.T
	mu sync.Mutex

	tokenCalls, refreshCalls int
	tokenSeq                 int
	valid                    map[string]bool // access tokens accepted
	expiresIn                int64           // value returned as expires_in
	lastInvoice              map[string]any
	paid                     map[string]string // invoice_id → status
	paidAmount               map[string]string
	rejectNext401            bool
}

func newFake(t *testing.T) (*fakeQPay, *httptest.Server) {
	f := &fakeQPay{t: t, valid: map[string]bool{}, paid: map[string]string{}, paidAmount: map[string]string{}, expiresIn: 3600}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeQPay) issue(w http.ResponseWriter) {
	f.tokenSeq++
	tok := fmt.Sprintf("access-%d", f.tokenSeq)
	f.valid[tok] = true
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token_type": "bearer", "access_token": tok, "expires_in": f.expiresIn,
		"refresh_token": fmt.Sprintf("refresh-%d", f.tokenSeq), "refresh_expires_in": 86400,
	})
}

func (f *fakeQPay) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/v2/auth/token":
		u, p, ok := r.BasicAuth()
		if !ok || u != "client" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"CLIENT_NOTFOUND"}`))
			return
		}
		f.tokenCalls++
		f.issue(w)
		return
	case "/v2/auth/refresh":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer refresh-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.refreshCalls++
		f.issue(w)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !f.valid[tok] || f.rejectNext401 {
		f.rejectNext401 = false
		delete(f.valid, tok)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"NO_CREDENDIALS"}`))
		return
	}
	var body map[string]any
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))
	switch r.URL.Path {
	case "/v2/invoice":
		f.lastInvoice = body
		id := "qpay-inv-" + body["sender_invoice_no"].(string)[:8]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"invoice_id": id, "qr_text": "0002010102121531...", "qr_image": "iVBORw0KGgo=",
			"qPay_shortUrl": "https://s.qpay.mn/abc",
			"urls": []map[string]any{
				{"name": "Khan bank", "description": "Хаан банк", "logo": "https://qpay.mn/q/logo/khanbank.png", "link": "khanbank://q?qPay_QRcode=x"},
				{"name": "Golomt bank", "description": "", "logo": "https://qpay.mn/q/logo/golomt.png", "link": "golomtbank://q?qPay_QRcode=x"},
			},
		})
	case "/v2/payment/check":
		assert.Equal(f.t, "INVOICE", body["object_type"])
		id, _ := body["object_id"].(string)
		st, ok := f.paid[id]
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"count": 0, "paid_amount": 0, "rows": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count": 1, "paid_amount": f.paidAmount[id],
			"rows": []map[string]any{{"payment_id": "593", "payment_status": st, "payment_amount": f.paidAmount[id], "payment_currency": "MNT"}},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newClient(srv *httptest.Server, now func() time.Time) *qpay.Client {
	return qpay.New(qpay.Config{
		BaseURL: srv.URL + "/v2/", Username: "client", Password: "secret", InvoiceCode: "CALLGO_INVOICE",
		CallbackURL: "https://api.callgo.mn/api/billing/webhooks/qpay?src=qpay", Client: srv.Client(), Now: now,
	})
}

func TestQPayCreateAndCheck(t *testing.T) {
	ctx := context.Background()
	fake, srv := newFake(t)
	c := newClient(srv, nil)
	assert.Equal(t, "qpay", c.Name())

	pay := &domain.Payment{ID: uuid.New(), OrgID: uuid.New(), AmountMNT: 319_000}
	require.NoError(t, c.CreateInvoice(ctx, pay, "CallGo.mn нэхэмжлэх CG-2026-000001"))
	assert.Equal(t, "qpay-inv-"+pay.ID.String()[:8], pay.ProviderRef)
	assert.Equal(t, "0002010102121531...", pay.QRText)
	assert.Equal(t, "iVBORw0KGgo=", pay.QRImage)
	require.Len(t, pay.DeepLinks, 2)
	assert.Equal(t, domain.PaymentLink{Name: "Хаан банк", Logo: "https://qpay.mn/q/logo/khanbank.png", Link: "khanbank://q?qPay_QRcode=x"}, pay.DeepLinks[0])
	assert.Equal(t, "Golomt bank", pay.DeepLinks[1].Name)

	inv := fake.lastInvoice
	assert.Equal(t, "CALLGO_INVOICE", inv["invoice_code"])
	assert.Equal(t, pay.ID.String(), inv["sender_invoice_no"])
	assert.Equal(t, pay.OrgID.String(), inv["invoice_receiver_code"])
	assert.Equal(t, float64(319_000), inv["amount"])
	cb, err := url.Parse(inv["callback_url"].(string))
	require.NoError(t, err)
	assert.Equal(t, pay.ID.String(), cb.Query().Get("payment_id"))
	assert.Equal(t, "qpay", cb.Query().Get("src"))

	st, err := c.Check(ctx, pay)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, st)

	fake.mu.Lock()
	fake.paid[pay.ProviderRef] = "PAID"
	fake.paidAmount[pay.ProviderRef] = "100000.00"
	fake.mu.Unlock()
	st, err = c.Check(ctx, pay)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, st, "partial payment")

	fake.mu.Lock()
	fake.paidAmount[pay.ProviderRef] = "319000.00"
	fake.mu.Unlock()
	st, err = c.Check(ctx, pay)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPaid, st)

	fake.mu.Lock()
	assert.Equal(t, 1, fake.tokenCalls, "token cached across calls")
	fake.mu.Unlock()

	ref, err := c.VerifyCallback(ctx, map[string]string{"payment_id": pay.ID.String()}, nil)
	require.NoError(t, err)
	assert.Equal(t, pay.ID.String(), ref)
	_, err = c.VerifyCallback(ctx, map[string]string{}, []byte(`{}`))
	assert.Error(t, err)
}

func TestQPayRetriesOn401(t *testing.T) {
	ctx := context.Background()
	fake, srv := newFake(t)
	c := newClient(srv, nil)
	pay := &domain.Payment{ID: uuid.New(), OrgID: uuid.New(), AmountMNT: 1000}
	require.NoError(t, c.CreateInvoice(ctx, pay, "x"))

	fake.mu.Lock()
	fake.rejectNext401 = true
	fake.mu.Unlock()
	st, err := c.Check(ctx, pay)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, st)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Equal(t, 1, fake.refreshCalls, "401 → refresh token used")
}

func TestQPayRefreshesExpiredToken(t *testing.T) {
	ctx := context.Background()
	fake, srv := newFake(t)
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	c := newClient(srv, func() time.Time { return now })
	fake.expiresIn = now.Add(time.Hour).Unix() // absolute timestamp form
	pay := &domain.Payment{ID: uuid.New(), OrgID: uuid.New(), AmountMNT: 1000}
	require.NoError(t, c.CreateInvoice(ctx, pay, "x"))
	_, err := c.Check(ctx, pay)
	require.NoError(t, err)
	fake.mu.Lock()
	assert.Equal(t, 1, fake.tokenCalls)
	assert.Equal(t, 0, fake.refreshCalls)
	fake.expiresIn = 3600 // relative form
	fake.mu.Unlock()

	now = now.Add(2 * time.Hour)
	_, err = c.Check(ctx, pay)
	require.NoError(t, err)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Equal(t, 1, fake.tokenCalls)
	assert.Equal(t, 1, fake.refreshCalls)
}

func TestQPayErrors(t *testing.T) {
	ctx := context.Background()
	_, srv := newFake(t)
	bad := qpay.New(qpay.Config{BaseURL: srv.URL + "/v2", Username: "client", Password: "wrong", InvoiceCode: "X", CallbackURL: "https://x.mn/cb", Client: srv.Client()})
	err := bad.CreateInvoice(ctx, &domain.Payment{ID: uuid.New(), AmountMNT: 10}, "x")
	var ae *qpay.APIError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusUnauthorized, ae.Status)

	noCB := qpay.New(qpay.Config{BaseURL: srv.URL + "/v2", Username: "client", Password: "secret", Client: srv.Client()})
	assert.Error(t, noCB.CreateInvoice(ctx, &domain.Payment{ID: uuid.New(), AmountMNT: 10}, "x"))
	c := newClient(srv, nil)
	assert.Error(t, c.CreateInvoice(ctx, &domain.Payment{ID: uuid.New()}, "zero amount"))
	_, err = c.Check(ctx, &domain.Payment{})
	assert.Error(t, err)
}

func TestFromEnv(t *testing.T) {
	env := map[string]string{
		"QPAY_BASE_URL": "https://merchant-sandbox.qpay.mn/v2", "QPAY_USERNAME": "u", "QPAY_PASSWORD": "p",
		"QPAY_INVOICE_CODE": "C", "QPAY_CALLBACK_URL": "https://x.mn/cb",
	}
	cfg := qpay.FromEnv(func(k string) string { return env[k] })
	assert.True(t, cfg.Configured())
	assert.Equal(t, "https://merchant-sandbox.qpay.mn/v2", cfg.BaseURL)
	assert.False(t, qpay.FromEnv(func(string) string { return "" }).Configured())
}
