package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// AdminAuditWriter records platform-admin actions.
type AdminAuditWriter interface {
	Append(ctx context.Context, e *domain.AuditEntry) error
}

// AdminDeps are the collaborators of the platform-admin API
// (docs/API.md "Platform admin"). Identity, Billing, Users, Plans,
// SetSubscription and MarkInvoicePaid are required; nil Audit skips auditing.
type AdminDeps struct {
	Identity domain.IdentityRepository
	Billing  domain.BillingRepository
	// Users loads the acting user (IsPlatformAdmin), orgs and org users.
	Users domain.OrgRepository
	// Calls is reserved for call-level drill-downs; the current endpoints
	// derive call counts from Billing usage.
	Calls domain.CallRepository
	// Plans resolves a plan by code.
	Plans func(code string) (domain.Plan, bool)
	// SetSubscription changes an org's plan / status / limits / period end
	// (the billing service; it also keeps Organization.PlanCode in sync).
	SetSubscription func(ctx context.Context, orgID uuid.UUID, planCode string, status *domain.SubscriptionStatus, custom *domain.Plan, periodEnd *time.Time) (*domain.Subscription, error)
	// MarkInvoicePaid settles an invoice paid by bank transfer.
	MarkInvoicePaid func(ctx context.Context, invoiceID uuid.UUID, note string) (*domain.Invoice, error)
	Audit           AdminAuditWriter
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// adminTZ is the timezone that defines "today" for platform statistics.
const adminTZ = "Asia/Ulaanbaatar"

const adminStatsPage = 200

type adminHandlers struct {
	d   AdminDeps
	cfg Config
	log zerolog.Logger
	s   *server // error mapping only
}

// mountAdmin mounts the platform-admin routes under /api/admin. Every route
// requires a valid token of a user with IsPlatformAdmin.
func mountAdmin(r chi.Router, d AdminDeps, cfg Config, log zerolog.Logger) {
	h := &adminHandlers{d: d, cfg: cfg, log: log, s: &server{log: log}}
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(auth.RequireAuth(cfg.JWTSecret))
		r.Use(auth.RequirePlatformAdmin(h.isPlatformAdmin))
		r.Get("/stats", h.stats)
		r.Get("/orgs", h.listOrgs)
		r.Get("/orgs/{id}", h.getOrg)
		r.Put("/orgs/{id}", h.updateOrg)
		r.Put("/orgs/{id}/subscription", h.setSubscription)
		r.Post("/invoices/{id}/mark-paid", h.markPaid)
	})
}

func (h *adminHandlers) now() time.Time {
	if h.d.Now != nil {
		return h.d.Now()
	}
	return time.Now()
}

// isPlatformAdmin is the auth.PlatformAdminCheck: the acting user must exist,
// be enabled and carry IsPlatformAdmin (re-read on every request so revoking
// the flag takes effect immediately).
func (h *adminHandlers) isPlatformAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	if h.d.Users == nil {
		return false, errNotConfigured("admin")
	}
	u, err := h.d.Users.GetUser(ctx, userID)
	if err != nil {
		return false, err
	}
	return u.IsPlatformAdmin && u.Status != domain.UserDisabled, nil
}

func (h *adminHandlers) ready() error {
	if h.d.Identity == nil || h.d.Billing == nil || h.d.Users == nil {
		return errNotConfigured("admin")
	}
	return nil
}

func (h *adminHandlers) audit(r *http.Request, orgID uuid.UUID, action, targetType, targetID string, meta map[string]any) {
	if h.d.Audit == nil {
		return
	}
	e := &domain.AuditEntry{
		ID: uuid.New(), OrgID: orgID, Action: action, TargetType: targetType, TargetID: targetID,
		Meta: meta, IP: adminClientIP(r), At: h.now().UTC(),
	}
	if c, ok := auth.FromContext(r.Context()); ok {
		id := c.UserID
		e.ActorID = &id
		if h.d.Users != nil {
			if u, err := h.d.Users.GetUser(r.Context(), id); err == nil {
				e.ActorEmail = u.Email
			}
		}
	}
	if err := h.d.Audit.Append(r.Context(), e); err != nil {
		h.log.Warn().Err(err).Str("action", action).Msg("admin audit append failed")
	}
}

func adminClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- response shapes -------------------------------------------------------

type adminOrgRow struct {
	Org          domain.Organization  `json:"org"`
	Subscription *domain.Subscription `json:"subscription"`
	Usage        domain.UsageSummary  `json:"usage"`
	Users        int                  `json:"users"`
}

// usageWindow is the subscription's billing period, or the current month when
// the org has no subscription.
func (h *adminHandlers) usageWindow(sub *domain.Subscription) (time.Time, time.Time) {
	now := h.now().UTC()
	if sub != nil && !sub.CurrentPeriodStart.IsZero() && !sub.CurrentPeriodEnd.IsZero() {
		return sub.CurrentPeriodStart, sub.CurrentPeriodEnd
	}
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// subscriptionOf returns nil (no error) when the org has none.
func (h *adminHandlers) subscriptionOf(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	sub, err := h.d.Billing.GetSubscription(ctx, orgID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return sub, err
}

func (h *adminHandlers) orgRow(ctx context.Context, o domain.Organization) (adminOrgRow, error) {
	sub, err := h.subscriptionOf(ctx, o.ID)
	if err != nil {
		return adminOrgRow{}, err
	}
	from, to := h.usageWindow(sub)
	usage, err := h.d.Billing.SummarizeUsage(ctx, o.ID, from, to)
	if err != nil {
		return adminOrgRow{}, err
	}
	users, err := h.d.Identity.CountUsers(ctx, o.ID)
	if err != nil {
		return adminOrgRow{}, err
	}
	return adminOrgRow{Org: o, Subscription: sub, Usage: usage, Users: users}, nil
}

// ---- handlers ---------------------------------------------------------------

func (h *adminHandlers) listOrgs(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	orgs, total, err := h.d.Identity.ListOrgs(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	rows := make([]adminOrgRow, 0, len(orgs))
	for _, o := range orgs {
		row, err := h.orgRow(r.Context(), o)
		if err != nil {
			h.s.writeErr(w, r, err)
			return
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, newList(rows, total))
}

func (h *adminHandlers) getOrg(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	org, err := h.d.Users.GetOrg(ctx, id)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	sub, err := h.subscriptionOf(ctx, id)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	from, to := h.usageWindow(sub)
	usage, err := h.d.Billing.SummarizeUsage(ctx, id, from, to)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	users, err := h.d.Users.ListUsers(ctx, id)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	invoices, err := h.d.Billing.ListInvoices(ctx, id)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	if users == nil {
		users = []domain.User{}
	}
	if invoices == nil {
		invoices = []domain.Invoice{}
	}
	var plan *domain.Plan
	code := org.PlanCode
	if sub != nil {
		code = sub.PlanCode
	}
	if p, ok := h.plan(code); ok {
		if sub != nil && sub.CustomLimits != nil {
			p = *sub.CustomLimits
		}
		plan = &p
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"org": org, "subscription": sub, "plan": plan, "usage": usage, "users": users, "invoices": invoices,
	})
}

func (h *adminHandlers) plan(code string) (domain.Plan, bool) {
	if h.d.Plans == nil || code == "" {
		return domain.Plan{}, false
	}
	return h.d.Plans(code)
}

func (h *adminHandlers) updateOrg(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	var req struct {
		Status domain.OrgStatus `json:"status"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	switch req.Status {
	case domain.OrgActive, domain.OrgSuspended, domain.OrgClosed:
	default:
		h.s.writeErr(w, r, errInvalid("status must be active, suspended or closed"))
		return
	}
	org, err := h.d.Users.GetOrg(r.Context(), id)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	prev := org.Status
	org.Status = req.Status
	org.UpdatedAt = h.now().UTC()
	if err := h.d.Identity.UpdateOrg(r.Context(), org); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	h.audit(r, id, "admin.org.status", "org", id.String(), map[string]any{"from": string(prev), "to": string(req.Status)})
	writeJSON(w, http.StatusOK, map[string]any{"org": org})
}

func (h *adminHandlers) setSubscription(w http.ResponseWriter, r *http.Request) {
	if h.d.SetSubscription == nil || h.d.Users == nil {
		h.s.writeErr(w, r, errNotConfigured("admin"))
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	var req struct {
		PlanCode         string                     `json:"planCode"`
		Status           *domain.SubscriptionStatus `json:"status"`
		CustomLimits     *domain.Plan               `json:"customLimits"`
		CurrentPeriodEnd *time.Time                 `json:"currentPeriodEnd"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	req.PlanCode = strings.TrimSpace(req.PlanCode)
	if req.PlanCode == "" {
		h.s.writeErr(w, r, errInvalid("planCode is required"))
		return
	}
	if _, ok := h.plan(req.PlanCode); !ok {
		h.s.writeErr(w, r, errInvalid("unknown plan %q", req.PlanCode))
		return
	}
	if req.Status != nil {
		switch *req.Status {
		case domain.SubTrialing, domain.SubActive, domain.SubPastDue, domain.SubCanceled:
		default:
			h.s.writeErr(w, r, errInvalid("invalid subscription status"))
			return
		}
	}
	if req.CurrentPeriodEnd != nil && !req.CurrentPeriodEnd.After(h.now()) {
		h.s.writeErr(w, r, errInvalid("currentPeriodEnd must be in the future"))
		return
	}
	if _, err := h.d.Users.GetOrg(r.Context(), id); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	sub, err := h.d.SetSubscription(r.Context(), id, req.PlanCode, req.Status, req.CustomLimits, req.CurrentPeriodEnd)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	meta := map[string]any{"planCode": req.PlanCode}
	if req.Status != nil {
		meta["status"] = string(*req.Status)
	}
	if req.CustomLimits != nil {
		meta["customLimits"] = true
	}
	if req.CurrentPeriodEnd != nil {
		meta["currentPeriodEnd"] = req.CurrentPeriodEnd.UTC().Format(time.RFC3339)
	}
	h.audit(r, id, "admin.subscription.set", "subscription", id.String(), meta)
	writeJSON(w, http.StatusOK, map[string]any{"subscription": sub})
}

func (h *adminHandlers) markPaid(w http.ResponseWriter, r *http.Request) {
	if h.d.MarkInvoicePaid == nil {
		h.s.writeErr(w, r, errNotConfigured("admin"))
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	inv, err := h.d.MarkInvoicePaid(r.Context(), id, strings.TrimSpace(req.Note))
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	h.audit(r, inv.OrgID, "admin.invoice.mark_paid", "invoice", id.String(), map[string]any{"number": inv.Number, "note": req.Note})
	writeJSON(w, http.StatusOK, map[string]any{"invoice": inv})
}

// stats aggregates platform-wide numbers. Usage "today" is summed per org
// (Billing has no cross-org summary), iterating ListOrgs in pages.
func (h *adminHandlers) stats(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(); err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	loc, err := time.LoadLocation(adminTZ)
	if err != nil {
		loc = time.FixedZone("ULAT", 8*3600)
	}
	now := h.now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)

	var (
		orgsTotal, callsToday int
		minutesToday          float64
	)
	for offset := 0; ; offset += adminStatsPage {
		orgs, total, err := h.d.Identity.ListOrgs(ctx, "", adminStatsPage, offset)
		if err != nil {
			h.s.writeErr(w, r, err)
			return
		}
		orgsTotal = total
		for _, o := range orgs {
			u, err := h.d.Billing.SummarizeUsage(ctx, o.ID, from, to)
			if err != nil {
				h.s.writeErr(w, r, err)
				return
			}
			callsToday += u.Calls
			minutesToday += u.Minutes
		}
		if len(orgs) < adminStatsPage || offset+len(orgs) >= total {
			break
		}
	}

	subs, err := h.d.Billing.ListSubscriptions(ctx, domain.SubActive)
	if err != nil {
		h.s.writeErr(w, r, err)
		return
	}
	var mrr int64
	active := 0
	for _, s := range subs {
		if s.Status != domain.SubActive {
			continue
		}
		active++
		switch {
		case s.CustomLimits != nil:
			mrr += s.CustomLimits.MonthlyMNT
		default:
			if p, ok := h.plan(s.PlanCode); ok {
				mrr += p.MonthlyMNT
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orgs": orgsTotal, "activeSubscriptions": active, "mrrMnt": mrr,
		"callsToday": callsToday, "minutesToday": minutesToday,
	})
}
