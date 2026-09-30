package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// BillingAuditFunc appends an audit entry. actorID is nil for provider
// callbacks.
type BillingAuditFunc func(ctx context.Context, orgID uuid.UUID, actorID *uuid.UUID, action, targetType, targetID string, meta map[string]any)

// BillingDeps are the collaborators of the /api/billing routes.
type BillingDeps struct {
	Svc *billing.Service
	// Audit records mutations (optional).
	Audit BillingAuditFunc
	// Auth authenticates the non-public billing routes. Default:
	// auth.RequireAuth(cfg.JWTSecret). Set it when API-key auth is in use.
	Auth func(http.Handler) http.Handler
}

// billingMaxCallbackBody bounds provider callback bodies.
const billingMaxCallbackBody = 64 << 10

type billingAPI struct {
	d   BillingDeps
	log zerolog.Logger
}

// mountBilling registers docs/API.md "Billing" on r, which must be the
// "/api" sub-router (outside the authenticated group): the plan list and the
// provider webhook are public, everything else is authenticated here.
//
//	GET  /billing/plans                    public
//	POST /billing/webhooks/{provider}      public (GET accepted too)
//	GET  /billing/subscription
//	POST /billing/subscription             owner/admin
//	POST /billing/subscription/cancel      owner/admin
//	GET  /billing/usage?from=&to=
//	GET  /billing/invoices
//	GET  /billing/invoices/{id}
//	GET  /billing/invoices/{id}/pdf        text/html (printable)
//	POST /billing/invoices/{id}/pay        owner/admin
//	GET  /billing/payments/{id}
func mountBilling(r chi.Router, d BillingDeps, cfg Config, log zerolog.Logger) {
	h := &billingAPI{d: d, log: log.With().Str("component", "httpapi.billing").Logger()}
	authMW := d.Auth
	if authMW == nil {
		authMW = auth.RequireAuth(cfg.JWTSecret)
	}
	admin := auth.RequireRole(domain.RoleOwner, domain.RoleAdmin)
	r.Route("/billing", func(r chi.Router) {
		r.Get("/plans", h.plans)
		r.Post("/webhooks/{provider}", h.webhook)
		r.Get("/webhooks/{provider}", h.webhook)

		r.Group(func(r chi.Router) {
			r.Use(authMW)
			r.Get("/subscription", h.getSubscription)
			r.With(admin).Post("/subscription", h.changePlan)
			r.With(admin).Post("/subscription/cancel", h.cancel)
			r.Get("/usage", h.usage)
			r.Get("/invoices", h.listInvoices)
			r.Get("/invoices/{id}", h.getInvoice)
			r.Get("/invoices/{id}/pdf", h.invoiceDocument)
			r.With(admin).Post("/invoices/{id}/pay", h.pay)
			r.Get("/payments/{id}", h.getPayment)
		})
	})
}

func (h *billingAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		auth.WriteError(w, ae.status, ae.code, ae.message)
	case errors.Is(err, billing.ErrProvider):
		h.log.Error().Err(err).Str("path", r.URL.Path).Str("reqId", middleware.GetReqID(r.Context())).Msg("payment provider error")
		auth.WriteError(w, http.StatusBadGateway, "internal", "payment provider unavailable")
	case errors.Is(err, domain.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrConflict):
		auth.WriteError(w, http.StatusConflict, "conflict", billingPublicMessage(err, domain.ErrConflict))
	case errors.Is(err, domain.ErrInvalid):
		auth.WriteError(w, http.StatusBadRequest, "invalid", billingPublicMessage(err, domain.ErrInvalid))
	case errors.Is(err, domain.ErrUnauthorized):
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
	case errors.Is(err, domain.ErrForbidden):
		auth.WriteError(w, http.StatusForbidden, "forbidden", "forbidden")
	default:
		h.log.Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).
			Str("reqId", middleware.GetReqID(r.Context())).Msg("billing request failed")
		auth.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// billingPublicMessage returns the part of err's message from the sentinel on
// ("invalid input: plan is not available ..."), hiding wrapping prefixes.
func billingPublicMessage(err, sentinel error) string {
	msg := err.Error()
	if i := strings.Index(msg, sentinel.Error()); i >= 0 {
		return msg[i:]
	}
	return sentinel.Error()
}

func (h *billingAPI) svc() (*billing.Service, error) {
	if h.d.Svc == nil {
		return nil, errNotConfigured("billing")
	}
	return h.d.Svc, nil
}

func (h *billingAPI) audit(ctx context.Context, orgID uuid.UUID, actor *uuid.UUID, action, targetType, targetID string, meta map[string]any) {
	if h.d.Audit == nil {
		return
	}
	h.d.Audit(ctx, orgID, actor, action, targetType, targetID, meta)
}

func billingActor(c auth.Claims) *uuid.UUID {
	if c.UserID == uuid.Nil {
		return nil
	}
	id := c.UserID
	return &id
}

func (h *billingAPI) plans(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, newList(billing.Plans.Public(), len(billing.Plans.Public())))
}

func (h *billingAPI) getSubscription(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ov, err := svc.Get(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (h *billingAPI) changePlan(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var in struct {
		PlanCode string `json:"planCode"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	in.PlanCode = strings.TrimSpace(in.PlanCode)
	if in.PlanCode == "" {
		h.fail(w, r, errInvalid("planCode is required"))
		return
	}
	c := claimsOf(r)
	res, err := svc.ChangePlan(r.Context(), c.OrgID, in.PlanCode)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	meta := map[string]any{"planCode": in.PlanCode, "status": res.Subscription.Status}
	if res.Invoice != nil {
		meta["invoiceId"] = res.Invoice.ID
		meta["invoiceNumber"] = res.Invoice.Number
	}
	if res.PendingPlanCode != "" {
		meta["effective"] = "period_end"
	}
	h.audit(r.Context(), c.OrgID, billingActor(c), "subscription.update", "subscription", res.Subscription.ID.String(), meta)
	writeJSON(w, http.StatusOK, res)
}

func (h *billingAPI) cancel(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c := claimsOf(r)
	sub, err := svc.Cancel(r.Context(), c.OrgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.audit(r.Context(), c.OrgID, billingActor(c), "subscription.cancel", "subscription", sub.ID.String(),
		map[string]any{"planCode": sub.PlanCode, "status": sub.Status})
	writeJSON(w, http.StatusOK, map[string]any{"subscription": sub})
}

// billingTimeParam parses an RFC 3339 timestamp or a YYYY-MM-DD date (UTC).
func billingTimeParam(r *http.Request, name string) (time.Time, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, errInvalid("%s must be RFC 3339 or YYYY-MM-DD", name)
}

func (h *billingAPI) usage(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	from, err := billingTimeParam(r, "from")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	to, err := billingTimeParam(r, "to")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rep, err := svc.Usage(r.Context(), claimsOf(r).OrgID, from, to)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *billingAPI) listInvoices(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	invs, err := svc.Invoices(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newList(invs, len(invs)))
}

func (h *billingAPI) getInvoice(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	inv, pays, err := svc.Invoice(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if pays == nil {
		pays = []domain.Payment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"invoice": inv, "payments": pays})
}

// invoiceDocument serves the printable invoice. docs/API.md calls it a PDF;
// it is HTML because the standard PDF fonts cannot render Cyrillic (see
// billing.InvoiceHTML).
func (h *billingAPI) invoiceDocument(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	orgID := claimsOf(r).OrgID
	inv, _, err := svc.Invoice(r.Context(), orgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	org, err := svc.Org(r.Context(), orgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	body := billing.InvoiceHTML(inv, org)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.html"`, inv.Number))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

func (h *billingAPI) pay(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var in struct {
		Provider string `json:"provider"`
	}
	if err := decodeOptionalJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	c := claimsOf(r)
	p, err := svc.Pay(r.Context(), c.OrgID, id, strings.TrimSpace(in.Provider))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.audit(r.Context(), c.OrgID, billingActor(c), "invoice.pay", "invoice", id.String(),
		map[string]any{"paymentId": p.ID, "provider": p.Provider, "amountMnt": p.AmountMNT})
	writeJSON(w, http.StatusCreated, map[string]any{"payment": p})
}

func (h *billingAPI) getPayment(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := svc.PaymentForOrg(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payment": p})
}

// webhook handles a provider callback: 200 "SUCCESS" once the payment is
// confirmed with the provider (paid or still pending).
func (h *billingAPI) webhook(w http.ResponseWriter, r *http.Request) {
	svc, err := h.svc()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	provider := chi.URLParam(r, "provider")
	query := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, billingMaxCallbackBody))
	if err != nil {
		h.fail(w, r, errInvalid("callback body too large"))
		return
	}
	p, settled, err := svc.HandleCallback(r.Context(), provider, query, body)
	if err != nil {
		h.log.Warn().Err(err).Str("provider", provider).Msg("payment callback rejected")
		h.fail(w, r, err)
		return
	}
	if settled {
		h.audit(r.Context(), p.OrgID, nil, "payment.paid", "payment", p.ID.String(),
			map[string]any{"provider": p.Provider, "invoiceId": p.InvoiceID, "amountMnt": p.AmountMNT, "via": "callback"})
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "SUCCESS")
}
