package billing

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Event types published by billing (docs/EVENTS.md "SaaS additions"). They
// are not yet constants in domain.go.
const (
	EventBillingUpdated domain.EventType = "billing.updated"
	EventQuotaWarning   domain.EventType = "quota.warning"
)

// Errors returned by the service. They wrap the domain sentinels so the HTTP
// layer maps them onto 400 / 409.
var (
	ErrPlanUnavailable = fmt.Errorf("%w: plan is not available for self-service", domain.ErrInvalid)
	ErrUnknownProvider = fmt.Errorf("%w: unknown payment provider", domain.ErrInvalid)
	ErrAlreadyOnPlan   = fmt.Errorf("%w: already on this plan", domain.ErrConflict)
	ErrInvoiceNotOpen  = fmt.Errorf("%w: invoice is not open", domain.ErrConflict)
	ErrNothingToPay    = fmt.Errorf("%w: invoice total is zero", domain.ErrInvalid)
	// ErrProvider marks a failure talking to the payment provider.
	ErrProvider = errors.New("payment provider error")
)

// pendingPlanKey is the Organization.Settings key holding a downgrade that
// takes effect at the end of the current period (the Subscription contract
// has no field for it).
const pendingPlanKey = "billingPendingPlan"

// OrgStore reads and updates organisations (status, denormalised plan code,
// settings). Implemented by the crm repositories.
type OrgStore interface {
	GetOrg(ctx context.Context, id uuid.UUID) (*domain.Organization, error)
	UpdateOrg(ctx context.Context, o *domain.Organization) error
}

// UsageChecker is optionally implemented by the BillingRepository to make
// RecordCall's idempotence check cheap. Without it the service scans
// ListUsage around the call's time window.
type UsageChecker interface {
	HasUsageForCall(ctx context.Context, callID uuid.UUID) (bool, error)
}

// ProviderCosts are internal cost estimates (MNT) used to fill
// UsageRecord.CostMNT.
type ProviderCosts struct {
	LLMPer1kTokensMNT float64
	STTPerMinMNT      float64
	TTSPer1kCharsMNT  float64
	// SIPPerMinMNT is the telephony cost per billed call minute (optional).
	SIPPerMinMNT float64
	// SMSPerMsgMNT is the SMS gateway cost per message (optional).
	SMSPerMsgMNT float64
}

// Config tunes the billing service. Zero values get the documented defaults.
type Config struct {
	VATPercent int // default 10
	DueDays    int // invoice due date = issue + DueDays; default 7
	// GraceDays after an invoice's due date before the org is suspended; a
	// past_due paying customer may still start calls during it. Default 7.
	GraceDays int
	// CheckAfter is the age after which GET /payments/{id} asks the provider
	// for a pending payment's status. Default 10s.
	CheckAfter    time.Duration
	Now           func() time.Time
	ProviderCosts ProviderCosts
	// ExtraProviders are additional payment providers selectable by name
	// (e.g. mock next to qpay). The provider passed to New is the default.
	ExtraProviders []domain.PaymentProvider
}

// Service implements billing: subscriptions, metering, entitlements,
// invoicing and payments. It is safe for concurrent use.
type Service struct {
	repo      domain.BillingRepository
	orgs      OrgStore
	providers map[string]domain.PaymentProvider
	defProv   string
	bus       domain.EventBus
	cfg       Config
	log       zerolog.Logger

	meterMu sync.Mutex // serialises RecordCall's check-then-insert
}

var _ domain.Entitlements = (*Service)(nil)

// New builds the billing service. provider may be nil (payments disabled
// unless ExtraProviders are given); bus may be nil (no events).
func New(repo domain.BillingRepository, orgs OrgStore, provider domain.PaymentProvider, bus domain.EventBus, cfg Config, log zerolog.Logger) *Service {
	if cfg.VATPercent <= 0 {
		cfg.VATPercent = 10
	}
	if cfg.DueDays <= 0 {
		cfg.DueDays = 7
	}
	if cfg.GraceDays <= 0 {
		cfg.GraceDays = 7
	}
	if cfg.CheckAfter <= 0 {
		cfg.CheckAfter = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Service{
		repo: repo, orgs: orgs, bus: bus, cfg: cfg,
		providers: map[string]domain.PaymentProvider{},
		log:       log.With().Str("component", "billing").Logger(),
	}
	if provider != nil {
		s.providers[provider.Name()] = provider
		s.defProv = provider.Name()
	}
	for _, p := range cfg.ExtraProviders {
		if p == nil {
			continue
		}
		s.providers[p.Name()] = p
		if s.defProv == "" {
			s.defProv = p.Name()
		}
	}
	return s
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Overview is the answer of GET /api/billing/subscription.
type Overview struct {
	Subscription *domain.Subscription `json:"subscription"`
	Plan         domain.Plan          `json:"plan"`
	Usage        domain.UsageSummary  `json:"usage"`
	Limits       domain.Plan          `json:"limits"`
	// PendingPlanCode is a downgrade scheduled for the end of the period.
	PendingPlanCode string `json:"pendingPlanCode,omitempty"`
}

// ChangeResult is the answer of POST /api/billing/subscription.
type ChangeResult struct {
	Subscription    *domain.Subscription `json:"subscription"`
	Invoice         *domain.Invoice      `json:"invoice,omitempty"`
	PendingPlanCode string               `json:"pendingPlanCode,omitempty"`
}

// StartTrial creates the 14-day trial subscription for a new org. It is
// idempotent: an existing subscription is returned unchanged.
func (s *Service) StartTrial(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	existing, err := s.getSub(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	trial, _ := Plans.Get(PlanTrial)
	now := s.now()
	end := now.AddDate(0, 0, trial.TrialDays)
	sub := &domain.Subscription{
		ID: uuid.New(), OrgID: orgID, PlanCode: PlanTrial, Status: domain.SubTrialing,
		CurrentPeriodStart: now, CurrentPeriodEnd: end, TrialEndsAt: &end,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
		return nil, fmt.Errorf("billing: start trial: %w", err)
	}
	if err := s.syncOrg(ctx, orgID, func(o *domain.Organization) bool {
		if o.PlanCode == PlanTrial {
			return false
		}
		o.PlanCode = PlanTrial
		return true
	}); err != nil {
		return nil, err
	}
	s.log.Info().Str("orgId", orgID.String()).Time("trialEndsAt", end).Msg("trial started")
	return sub, nil
}

// getSub returns the org's subscription or nil when it has none.
func (s *Service) getSub(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	sub, err := s.repo.GetSubscription(ctx, orgID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("billing: get subscription: %w", err)
	}
	return sub, nil
}

// subscription returns the org's subscription, starting a trial for orgs
// that predate billing.
func (s *Service) subscription(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	sub, err := s.getSub(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if sub != nil {
		return sub, nil
	}
	return s.StartTrial(ctx, orgID)
}

// planOf returns the catalog plan of sub (trial limits for unknown codes).
func (s *Service) planOf(sub *domain.Subscription) domain.Plan {
	p, ok := Plans.Get(sub.PlanCode)
	if !ok {
		s.log.Warn().Str("orgId", sub.OrgID.String()).Str("plan", sub.PlanCode).Msg("unknown plan code; using trial limits")
		p, _ = Plans.Get(PlanTrial)
	}
	return p
}

// EffectiveLimits returns the limits of sub: CustomLimits when set (with
// Code, Name and Features filled from the plan when empty), else the plan.
func (s *Service) EffectiveLimits(sub *domain.Subscription) domain.Plan {
	p := s.planOf(sub)
	if sub.CustomLimits == nil {
		return p
	}
	c := *sub.CustomLimits
	if c.Code == "" {
		c.Code = p.Code
	}
	if c.Name == "" {
		c.Name = p.Name
	}
	if c.Features == nil {
		c.Features = p.Features
	}
	return c
}

// summarize aggregates usage in [from, to) and fills the included/overage
// fields from limits.
func (s *Service) summarize(ctx context.Context, orgID uuid.UUID, from, to time.Time, limits domain.Plan) (domain.UsageSummary, error) {
	sum, err := s.repo.SummarizeUsage(ctx, orgID, from, to)
	if err != nil {
		return domain.UsageSummary{}, fmt.Errorf("billing: summarize usage: %w", err)
	}
	sum.OrgID, sum.PeriodStart, sum.PeriodEnd = orgID, from, to
	sum.IncludedMinutes = limits.IncludedMinutes
	sum.OverageMinutes, sum.OverageMNT = 0, 0
	if !Unlimited(limits.IncludedMinutes) && sum.Minutes > float64(limits.IncludedMinutes) {
		sum.OverageMinutes = math.Ceil(sum.Minutes - float64(limits.IncludedMinutes))
		sum.OverageMNT = int64(sum.OverageMinutes) * limits.OverageMNTPerMin
	}
	return sum, nil
}

// Get returns the org's subscription, plan, current-period usage and
// effective limits.
func (s *Service) Get(ctx context.Context, orgID uuid.UUID) (*Overview, error) {
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return nil, err
	}
	limits := s.EffectiveLimits(sub)
	usage, err := s.summarize(ctx, orgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, limits)
	if err != nil {
		return nil, err
	}
	ov := &Overview{Subscription: sub, Plan: s.planOf(sub), Usage: usage, Limits: limits}
	if org, err := s.orgs.GetOrg(ctx, orgID); err == nil {
		ov.PendingPlanCode = pendingPlan(org)
	}
	return ov, nil
}

// ChangePlan switches the org to a self-service paid plan:
//   - upgrade / from trial / resubscribe: a new period starts now and an open
//     invoice for the full month (plus overage of the cut-short paid period)
//     is created. The subscription is active immediately when the org was in
//     good standing (active, or trialing with the trial still running), else
//     past_due until the invoice is paid.
//   - downgrade from a paid plan: scheduled for the end of the period
//     (ChangeResult.PendingPlanCode); nothing is invoiced now.
//   - choosing the current plan again undoes a pending cancel or downgrade.
func (s *Service) ChangePlan(ctx context.Context, orgID uuid.UUID, code string) (*ChangeResult, error) {
	if !Plans.SelfService(code) {
		return nil, ErrPlanUnavailable
	}
	target, _ := Plans.Get(code)
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return nil, err
	}
	org, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: get org: %w", err)
	}
	now := s.now()
	cur := s.EffectiveLimits(sub)

	if sub.PlanCode == code && sub.Status != domain.SubCanceled {
		if sub.CanceledAt == nil && pendingPlan(org) == "" {
			return nil, ErrAlreadyOnPlan
		}
		sub.CanceledAt = nil
		sub.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
			return nil, fmt.Errorf("billing: update subscription: %w", err)
		}
		if err := s.setPendingPlan(ctx, org, ""); err != nil {
			return nil, err
		}
		s.publishUpdated(ctx, sub)
		return &ChangeResult{Subscription: sub}, nil
	}

	downgrade := sub.Status != domain.SubCanceled && sub.PlanCode != PlanTrial &&
		(sub.PlanCode == PlanEnterprise || target.MonthlyMNT < cur.MonthlyMNT)
	if downgrade {
		sub.CanceledAt = nil
		sub.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
			return nil, fmt.Errorf("billing: update subscription: %w", err)
		}
		if err := s.setPendingPlan(ctx, org, code); err != nil {
			return nil, err
		}
		s.publishUpdated(ctx, sub)
		return &ChangeResult{Subscription: sub, PendingPlanCode: code}, nil
	}

	trialRunning := sub.Status == domain.SubTrialing && (sub.TrialEndsAt == nil || now.Before(*sub.TrialEndsAt))
	goodStanding := trialRunning || sub.Status == domain.SubActive

	var carry []domain.InvoiceLine
	// Overage of the paid period that is being cut short.
	if sub.PlanCode != PlanTrial && sub.Status != domain.SubCanceled {
		usage, err := s.summarize(ctx, orgID, sub.CurrentPeriodStart, now, cur)
		if err != nil {
			return nil, err
		}
		if l, ok := overageLine(usage, cur); ok {
			carry = append(carry, l)
		}
	}
	// Void the unpaid advance invoice of the current period (superseded by the
	// new plan's invoice) and carry over its non-fee lines.
	invs, err := s.repo.ListInvoices(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: list invoices: %w", err)
	}
	for i := range invs {
		inv := invs[i]
		if inv.Status != domain.InvoiceOpen || !inv.PeriodStart.Equal(sub.CurrentPeriodStart) {
			continue
		}
		for _, l := range inv.Lines {
			if !isPlanFeeLine(l) {
				carry = append(carry, l)
			}
		}
		inv.Status = domain.InvoiceVoid
		if err := s.repo.UpdateInvoice(ctx, &inv); err != nil {
			return nil, fmt.Errorf("billing: void invoice: %w", err)
		}
	}

	start, end := now, addMonth(now)
	lines := append([]domain.InvoiceLine{planFeeLine(target, start, end)}, carry...)
	inv := s.newInvoice(orgID, start, end, lines, now)
	if err := s.repo.CreateInvoice(ctx, inv); err != nil {
		return nil, fmt.Errorf("billing: create invoice: %w", err)
	}

	sub.PlanCode = code
	sub.CustomLimits = nil
	sub.CurrentPeriodStart, sub.CurrentPeriodEnd = start, end
	sub.TrialEndsAt = nil
	sub.CanceledAt = nil
	sub.Status = domain.SubPastDue
	if goodStanding {
		sub.Status = domain.SubActive
	}
	sub.UpdatedAt = now
	if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
		return nil, fmt.Errorf("billing: update subscription: %w", err)
	}
	if err := s.syncOrg(ctx, orgID, func(o *domain.Organization) bool {
		changed := o.PlanCode != code || pendingPlan(o) != ""
		o.PlanCode = code
		delete(o.Settings, pendingPlanKey)
		return changed
	}); err != nil {
		return nil, err
	}
	s.log.Info().Str("orgId", orgID.String()).Str("plan", code).Str("status", string(sub.Status)).
		Str("invoice", inv.Number).Msg("plan changed")
	s.publishUpdated(ctx, sub)
	return &ChangeResult{Subscription: sub, Invoice: inv}, nil
}

// Cancel cancels the subscription at the end of the current period (a trial
// is canceled immediately). A pending downgrade is dropped.
func (s *Service) Cancel(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if sub.Status == domain.SubCanceled {
		return sub, nil
	}
	now := s.now()
	if sub.CanceledAt == nil {
		sub.CanceledAt = &now
	}
	if sub.PlanCode == PlanTrial {
		sub.Status = domain.SubCanceled
	}
	sub.UpdatedAt = now
	if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
		return nil, fmt.Errorf("billing: cancel subscription: %w", err)
	}
	if org, err := s.orgs.GetOrg(ctx, orgID); err == nil {
		if err := s.setPendingPlan(ctx, org, ""); err != nil {
			return nil, err
		}
	}
	s.publishUpdated(ctx, sub)
	return sub, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func pendingPlan(o *domain.Organization) string {
	if o == nil || o.Settings == nil {
		return ""
	}
	v, _ := o.Settings[pendingPlanKey].(string)
	return v
}

func (s *Service) setPendingPlan(ctx context.Context, org *domain.Organization, code string) error {
	if pendingPlan(org) == code {
		return nil
	}
	o := *org
	o.Settings = maps.Clone(org.Settings)
	if o.Settings == nil {
		o.Settings = map[string]any{}
	}
	if code == "" {
		delete(o.Settings, pendingPlanKey)
	} else {
		o.Settings[pendingPlanKey] = code
	}
	o.UpdatedAt = s.now()
	if err := s.orgs.UpdateOrg(ctx, &o); err != nil {
		return fmt.Errorf("billing: update org: %w", err)
	}
	*org = o
	return nil
}

// syncOrg loads the org, applies mutate and saves it when mutate reports a
// change.
func (s *Service) syncOrg(ctx context.Context, orgID uuid.UUID, mutate func(o *domain.Organization) bool) error {
	org, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("billing: get org: %w", err)
	}
	o := *org
	o.Settings = maps.Clone(org.Settings)
	if !mutate(&o) {
		return nil
	}
	o.UpdatedAt = s.now()
	if err := s.orgs.UpdateOrg(ctx, &o); err != nil {
		return fmt.Errorf("billing: update org: %w", err)
	}
	return nil
}

func (s *Service) publish(ctx context.Context, orgID uuid.UUID, typ domain.EventType, payload any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ctx, domain.Event{ID: uuid.NewString(), Type: typ, OrgID: orgID, At: s.now(), Payload: payload})
}

// publishUpdated sends billing.updated {subscription, usage}.
func (s *Service) publishUpdated(ctx context.Context, sub *domain.Subscription) {
	if s.bus == nil {
		return
	}
	payload := map[string]any{"subscription": sub}
	if usage, err := s.summarize(ctx, sub.OrgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, s.EffectiveLimits(sub)); err == nil {
		payload["usage"] = usage
	} else {
		s.log.Warn().Err(err).Str("orgId", sub.OrgID.String()).Msg("billing.updated without usage")
	}
	s.publish(ctx, sub.OrgID, EventBillingUpdated, payload)
}

// addMonth adds one calendar month, clamping the day (Jan 31 → Feb 28/29).
func addMonth(t time.Time) time.Time {
	y, m, d := t.Date()
	lastDay := time.Date(y, m+2, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(y, m+1, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}
