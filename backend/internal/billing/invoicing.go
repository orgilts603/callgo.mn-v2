package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	planFeePrefix = "Сарын төлбөр"
	overagePrefix = "Нэмэлт минут"
	dateLayout    = "2006-01-02"
	// maxCatchUpPeriods bounds how many missed periods one rollover run
	// invoices for a single subscription.
	maxCatchUpPeriods = 24
)

func planFeeLine(p domain.Plan, start, end time.Time) domain.InvoiceLine {
	return domain.InvoiceLine{
		Description: fmt.Sprintf("%s — %s багц (%s – %s)", planFeePrefix, p.Name, start.Format(dateLayout), end.Format(dateLayout)),
		Quantity:    1, UnitMNT: p.MonthlyMNT, AmountMNT: p.MonthlyMNT,
	}
}

func isPlanFeeLine(l domain.InvoiceLine) bool { return strings.HasPrefix(l.Description, planFeePrefix) }

// overageLine bills minutes above the included allowance.
func overageLine(u domain.UsageSummary, limits domain.Plan) (domain.InvoiceLine, bool) {
	if u.OverageMNT <= 0 {
		return domain.InvoiceLine{}, false
	}
	return domain.InvoiceLine{
		Description: fmt.Sprintf("%s (%d багтсан минутаас хэтэрсэн, %s – %s)", overagePrefix, limits.IncludedMinutes,
			u.PeriodStart.Format(dateLayout), u.PeriodEnd.Format(dateLayout)),
		Quantity: u.OverageMinutes, UnitMNT: limits.OverageMNTPerMin, AmountMNT: u.OverageMNT,
	}, true
}

// newInvoice builds an open invoice with subtotal, VAT and total computed.
func (s *Service) newInvoice(orgID uuid.UUID, start, end time.Time, lines []domain.InvoiceLine, now time.Time) *domain.Invoice {
	var sub int64
	for _, l := range lines {
		sub += l.AmountMNT
	}
	vat := VAT(sub, s.cfg.VATPercent)
	return &domain.Invoice{
		ID: uuid.New(), OrgID: orgID, PeriodStart: start, PeriodEnd: end, Lines: lines,
		SubtotalMNT: sub, VATMNT: vat, TotalMNT: sub + vat, Status: domain.InvoiceOpen,
		DueAt: now.AddDate(0, 0, s.cfg.DueDays), CreatedAt: now,
	}
}

// VAT returns percent of amount, rounded half up to a whole tögrög.
func VAT(amount int64, percent int) int64 {
	return (amount*int64(percent) + 50) / 100
}

// ---------------------------------------------------------------------------
// Period rollover and dunning (run by a scheduler, e.g. every 15 min)
// ---------------------------------------------------------------------------

// RunPeriodRollover processes every subscription whose period has ended:
//   - trial: status → past_due (org unchanged; calls are refused until a plan
//     is chosen);
//   - canceled at period end: final overage invoice (if any), status →
//     canceled;
//   - paid plan: invoice for the next period (plan fee in advance + overage of
//     the ended period + VAT), pending downgrade applied, period advanced.
//
// It returns the number of subscriptions changed. Per-org failures are
// logged and joined into the returned error; other orgs still proceed.
func (s *Service) RunPeriodRollover(ctx context.Context) (int, error) {
	now := s.now()
	var errs []error
	n := 0
	for _, st := range []domain.SubscriptionStatus{domain.SubTrialing, domain.SubActive, domain.SubPastDue} {
		subs, err := s.repo.ListSubscriptions(ctx, st)
		if err != nil {
			return n, fmt.Errorf("billing: list %s subscriptions: %w", st, err)
		}
		for i := range subs {
			sub := subs[i]
			if sub.CurrentPeriodEnd.After(now) {
				continue
			}
			changed, err := s.rollover(ctx, &sub, now)
			if err != nil {
				s.log.Error().Err(err).Str("orgId", sub.OrgID.String()).Msg("period rollover failed")
				errs = append(errs, fmt.Errorf("org %s: %w", sub.OrgID, err))
				continue
			}
			if changed {
				n++
			}
		}
	}
	return n, errors.Join(errs...)
}

func (s *Service) rollover(ctx context.Context, sub *domain.Subscription, now time.Time) (bool, error) {
	if sub.PlanCode == PlanTrial {
		if sub.Status != domain.SubTrialing {
			return false, nil // already expired
		}
		sub.Status = domain.SubPastDue
		sub.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
			return false, fmt.Errorf("expire trial: %w", err)
		}
		s.log.Info().Str("orgId", sub.OrgID.String()).Msg("trial expired")
		s.publishUpdated(ctx, sub)
		return true, nil
	}

	if sub.CanceledAt != nil {
		limits := s.EffectiveLimits(sub)
		usage, err := s.summarize(ctx, sub.OrgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, limits)
		if err != nil {
			return false, err
		}
		if l, ok := overageLine(usage, limits); ok {
			if err := s.createPeriodInvoice(ctx, sub.OrgID, sub.CurrentPeriodEnd, sub.CurrentPeriodEnd, []domain.InvoiceLine{l}, now); err != nil {
				return false, err
			}
		}
		sub.Status = domain.SubCanceled
		sub.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
			return false, fmt.Errorf("cancel subscription: %w", err)
		}
		s.log.Info().Str("orgId", sub.OrgID.String()).Msg("subscription canceled at period end")
		s.publishUpdated(ctx, sub)
		return true, nil
	}

	org, err := s.orgs.GetOrg(ctx, sub.OrgID)
	if err != nil {
		return false, fmt.Errorf("get org: %w", err)
	}
	for i := 0; i < maxCatchUpPeriods && !sub.CurrentPeriodEnd.After(now); i++ {
		limits := s.EffectiveLimits(sub)
		usage, err := s.summarize(ctx, sub.OrgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, limits)
		if err != nil {
			return false, err
		}
		next := *sub
		if pc := pendingPlan(org); pc != "" && pc != sub.PlanCode {
			if _, ok := Plans.Get(pc); ok {
				next.PlanCode = pc
				next.CustomLimits = nil
			}
		}
		next.CurrentPeriodStart = sub.CurrentPeriodEnd
		next.CurrentPeriodEnd = addMonth(sub.CurrentPeriodEnd)
		nextLimits := s.EffectiveLimits(&next)

		var lines []domain.InvoiceLine
		if nextLimits.MonthlyMNT > 0 {
			lines = append(lines, planFeeLine(nextLimits, next.CurrentPeriodStart, next.CurrentPeriodEnd))
		}
		if l, ok := overageLine(usage, limits); ok {
			lines = append(lines, l)
		}
		if len(lines) > 0 {
			if err := s.createPeriodInvoice(ctx, sub.OrgID, next.CurrentPeriodStart, next.CurrentPeriodEnd, lines, now); err != nil {
				return false, err
			}
		}
		next.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, &next); err != nil {
			return false, fmt.Errorf("advance period: %w", err)
		}
		if next.PlanCode != sub.PlanCode {
			code := next.PlanCode
			if err := s.syncOrg(ctx, sub.OrgID, func(o *domain.Organization) bool {
				o.PlanCode = code
				delete(o.Settings, pendingPlanKey)
				return true
			}); err != nil {
				return false, err
			}
			if org, err = s.orgs.GetOrg(ctx, sub.OrgID); err != nil {
				return false, fmt.Errorf("get org: %w", err)
			}
			s.log.Info().Str("orgId", sub.OrgID.String()).Str("plan", code).Msg("scheduled downgrade applied")
		}
		*sub = next
	}
	s.publishUpdated(ctx, sub)
	return true, nil
}

// createPeriodInvoice creates an invoice unless one already exists for the
// org and period start (idempotent re-runs).
func (s *Service) createPeriodInvoice(ctx context.Context, orgID uuid.UUID, start, end time.Time, lines []domain.InvoiceLine, now time.Time) error {
	existing, err := s.repo.GetInvoiceForPeriod(ctx, orgID, start)
	switch {
	case err == nil && existing != nil:
		return nil
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("find period invoice: %w", err)
	}
	inv := s.newInvoice(orgID, start, end, lines, now)
	if err := s.repo.CreateInvoice(ctx, inv); err != nil {
		return fmt.Errorf("create invoice: %w", err)
	}
	s.log.Info().Str("orgId", orgID.String()).Str("invoice", inv.Number).Int64("totalMnt", inv.TotalMNT).Msg("invoice issued")
	return nil
}

// RunDunning handles unpaid invoices: once the oldest open invoice is past
// its due date an active subscription becomes past_due; GraceDays after the
// due date the organisation is suspended. Returns the number of orgs changed.
func (s *Service) RunDunning(ctx context.Context) (int, error) {
	now := s.now()
	var errs []error
	n := 0
	for _, st := range []domain.SubscriptionStatus{domain.SubActive, domain.SubPastDue, domain.SubCanceled} {
		subs, err := s.repo.ListSubscriptions(ctx, st)
		if err != nil {
			return n, fmt.Errorf("billing: list %s subscriptions: %w", st, err)
		}
		for i := range subs {
			changed, err := s.dun(ctx, &subs[i], now)
			if err != nil {
				s.log.Error().Err(err).Str("orgId", subs[i].OrgID.String()).Msg("dunning failed")
				errs = append(errs, fmt.Errorf("org %s: %w", subs[i].OrgID, err))
				continue
			}
			if changed {
				n++
			}
		}
	}
	return n, errors.Join(errs...)
}

func (s *Service) dun(ctx context.Context, sub *domain.Subscription, now time.Time) (bool, error) {
	oldest, _, err := s.openInvoices(ctx, sub.OrgID)
	if err != nil {
		return false, err
	}
	if oldest == nil || now.Before(oldest.DueAt) {
		return false, nil
	}
	changed := false
	if sub.Status == domain.SubActive {
		sub.Status = domain.SubPastDue
		sub.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
			return false, fmt.Errorf("mark past_due: %w", err)
		}
		changed = true
	}
	if !now.Before(oldest.DueAt.AddDate(0, 0, s.cfg.GraceDays)) {
		suspended := false
		if err := s.syncOrg(ctx, sub.OrgID, func(o *domain.Organization) bool {
			if o.Status != domain.OrgActive {
				return false
			}
			o.Status = domain.OrgSuspended
			suspended = true
			return true
		}); err != nil {
			return false, err
		}
		if suspended {
			s.log.Warn().Str("orgId", sub.OrgID.String()).Str("invoice", oldest.Number).Msg("organization suspended for non-payment")
			changed = true
		}
	}
	if changed {
		s.publishUpdated(ctx, sub)
	}
	return changed, nil
}

// openInvoices returns the oldest-due open invoice (nil if none) and whether
// the org ever paid an invoice.
func (s *Service) openInvoices(ctx context.Context, orgID uuid.UUID) (*domain.Invoice, bool, error) {
	invs, err := s.repo.ListInvoices(ctx, orgID)
	if err != nil {
		return nil, false, fmt.Errorf("list invoices: %w", err)
	}
	var oldest *domain.Invoice
	paidEver := false
	for i := range invs {
		switch invs[i].Status {
		case domain.InvoicePaid:
			paidEver = true
		case domain.InvoiceOpen:
			if oldest == nil || invs[i].DueAt.Before(oldest.DueAt) {
				oldest = &invs[i]
			}
		}
	}
	return oldest, paidEver, nil
}

// ---------------------------------------------------------------------------
// Invoices and payments
// ---------------------------------------------------------------------------

// Invoices lists the org's invoices, newest first.
func (s *Service) Invoices(ctx context.Context, orgID uuid.UUID) ([]domain.Invoice, error) {
	invs, err := s.repo.ListInvoices(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: list invoices: %w", err)
	}
	sort.SliceStable(invs, func(i, j int) bool { return invs[i].CreatedAt.After(invs[j].CreatedAt) })
	return invs, nil
}

// Invoice returns one of the org's invoices with its payment attempts.
func (s *Service) Invoice(ctx context.Context, orgID, invoiceID uuid.UUID) (*domain.Invoice, []domain.Payment, error) {
	inv, err := s.orgInvoice(ctx, orgID, invoiceID)
	if err != nil {
		return nil, nil, err
	}
	ps, err := s.repo.ListPayments(ctx, invoiceID)
	if err != nil {
		return nil, nil, fmt.Errorf("billing: list payments: %w", err)
	}
	return inv, ps, nil
}

func (s *Service) orgInvoice(ctx context.Context, orgID, invoiceID uuid.UUID) (*domain.Invoice, error) {
	inv, err := s.repo.GetInvoice(ctx, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("billing: get invoice: %w", err)
	}
	if inv.OrgID != orgID {
		return nil, fmt.Errorf("billing: invoice: %w", domain.ErrNotFound)
	}
	return inv, nil
}

// Org returns an organisation (for the invoice document header).
func (s *Service) Org(ctx context.Context, orgID uuid.UUID) (*domain.Organization, error) {
	o, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("billing: get org: %w", err)
	}
	return o, nil
}

func (s *Service) provider(name string) (domain.PaymentProvider, error) {
	if name == "" {
		name = s.defProv
	}
	p, ok := s.providers[name]
	if !ok {
		return nil, ErrUnknownProvider
	}
	return p, nil
}

// Providers lists the configured payment provider names.
func (s *Service) Providers() []string {
	out := make([]string, 0, len(s.providers))
	for n := range s.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Pay starts a payment of an open invoice through the named provider ("" =
// default). A still-pending, unexpired payment with the same provider is
// reused instead of creating another provider invoice.
func (s *Service) Pay(ctx context.Context, orgID, invoiceID uuid.UUID, providerName string) (*domain.Payment, error) {
	prov, err := s.provider(providerName)
	if err != nil {
		return nil, err
	}
	inv, err := s.orgInvoice(ctx, orgID, invoiceID)
	if err != nil {
		return nil, err
	}
	if inv.Status != domain.InvoiceOpen {
		return nil, ErrInvoiceNotOpen
	}
	if inv.TotalMNT <= 0 {
		return nil, ErrNothingToPay
	}
	now := s.now()
	existing, err := s.repo.ListPayments(ctx, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("billing: list payments: %w", err)
	}
	for i := range existing {
		p := existing[i]
		if p.Provider == prov.Name() && p.Status == domain.PaymentPending && p.AmountMNT == inv.TotalMNT &&
			(p.ExpiresAt == nil || now.Before(*p.ExpiresAt)) {
			return &p, nil
		}
	}
	p := &domain.Payment{
		ID: uuid.New(), OrgID: orgID, InvoiceID: invoiceID, Provider: prov.Name(),
		AmountMNT: inv.TotalMNT, Status: domain.PaymentPending, CreatedAt: now,
	}
	desc := fmt.Sprintf("CallGo.mn нэхэмжлэх %s", inv.Number)
	if err := prov.CreateInvoice(ctx, p, desc); err != nil {
		return nil, fmt.Errorf("billing: %s create invoice: %w: %w", prov.Name(), ErrProvider, err)
	}
	if err := s.repo.CreatePayment(ctx, p); err != nil {
		return nil, fmt.Errorf("billing: create payment: %w", err)
	}
	s.log.Info().Str("orgId", orgID.String()).Str("payment", p.ID.String()).Str("provider", p.Provider).
		Int64("amountMnt", p.AmountMNT).Msg("payment started")
	return p, nil
}

// PaymentForOrg returns one of the org's payments. A pending payment older
// than Config.CheckAfter is re-checked with the provider first (the UI polls
// this every few seconds). Provider errors are logged, not returned.
func (s *Service) PaymentForOrg(ctx context.Context, orgID, paymentID uuid.UUID) (*domain.Payment, error) {
	p, err := s.repo.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, fmt.Errorf("billing: get payment: %w", err)
	}
	if p.OrgID != orgID {
		return nil, fmt.Errorf("billing: payment: %w", domain.ErrNotFound)
	}
	if p.Status != domain.PaymentPending || s.now().Sub(p.CreatedAt) < s.cfg.CheckAfter {
		return p, nil
	}
	checked, err := s.CheckPayment(ctx, p.ID)
	if err != nil {
		s.log.Warn().Err(err).Str("payment", p.ID.String()).Msg("payment check failed")
		return p, nil
	}
	return checked, nil
}

// CheckPayment asks the provider for a pending payment's status and settles
// it when paid (payment, invoice → paid; subscription and org → active).
func (s *Service) CheckPayment(ctx context.Context, paymentID uuid.UUID) (*domain.Payment, error) {
	p, err := s.repo.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, fmt.Errorf("billing: get payment: %w", err)
	}
	if p.Status != domain.PaymentPending {
		return p, nil
	}
	prov, ok := s.providers[p.Provider]
	if !ok {
		return p, ErrUnknownProvider
	}
	st, err := prov.Check(ctx, p)
	if err != nil {
		return p, fmt.Errorf("billing: %s check: %w: %w", p.Provider, ErrProvider, err)
	}
	now := s.now()
	if st == domain.PaymentPending && p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		st = domain.PaymentExpired
	}
	switch st {
	case domain.PaymentPaid:
		if err := s.settle(ctx, p, now); err != nil {
			return nil, err
		}
	case domain.PaymentFailed, domain.PaymentExpired:
		p.Status = st
		if err := s.repo.UpdatePayment(ctx, p); err != nil {
			return nil, fmt.Errorf("billing: update payment: %w", err)
		}
	}
	return p, nil
}

// HandleCallback processes a provider callback (e.g. QPay's). The callback
// only identifies the payment; its status is always confirmed with the
// provider's Check before anything is marked paid.
func (s *Service) HandleCallback(ctx context.Context, providerName string, query map[string]string, body []byte) (*domain.Payment, error) {
	prov, ok := s.providers[providerName]
	if !ok {
		return nil, ErrUnknownProvider
	}
	ref, err := prov.VerifyCallback(ctx, query, body)
	if err != nil {
		return nil, fmt.Errorf("billing: %s callback: %w: %w", providerName, domain.ErrInvalid, err)
	}
	var p *domain.Payment
	if id, perr := uuid.Parse(ref); perr == nil {
		p, err = s.repo.GetPayment(ctx, id)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("billing: get payment: %w", err)
		}
	}
	if p == nil {
		p, err = s.repo.GetPaymentByProviderRef(ctx, prov.Name(), ref)
		if err != nil {
			return nil, fmt.Errorf("billing: payment for callback: %w", err)
		}
	}
	if p.Provider != prov.Name() {
		return nil, fmt.Errorf("billing: payment for callback: %w", domain.ErrNotFound)
	}
	return s.CheckPayment(ctx, p.ID)
}

// MarkPaidManually settles an invoice paid outside a provider (bank
// transfer, platform admin). It records a "bank_transfer" payment carrying
// the note. Paying an already paid invoice is a no-op.
func (s *Service) MarkPaidManually(ctx context.Context, invoiceID uuid.UUID, note string) (*domain.Invoice, error) {
	inv, err := s.repo.GetInvoice(ctx, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("billing: get invoice: %w", err)
	}
	switch inv.Status {
	case domain.InvoicePaid:
		return inv, nil
	case domain.InvoiceOpen, domain.InvoiceDraft:
	default:
		return nil, ErrInvoiceNotOpen
	}
	now := s.now()
	p := &domain.Payment{
		ID: uuid.New(), OrgID: inv.OrgID, InvoiceID: inv.ID, Provider: "bank_transfer",
		AmountMNT: inv.TotalMNT, Status: domain.PaymentPending, CreatedAt: now,
		Raw: map[string]any{"note": note},
	}
	p.ProviderRef = "manual-" + p.ID.String()
	if err := s.repo.CreatePayment(ctx, p); err != nil {
		return nil, fmt.Errorf("billing: create payment: %w", err)
	}
	if err := s.settle(ctx, p, now); err != nil {
		return nil, err
	}
	return s.repo.GetInvoice(ctx, invoiceID)
}

// settle marks p and its invoice paid, then reactivates the subscription and
// the org when no open invoices remain.
func (s *Service) settle(ctx context.Context, p *domain.Payment, now time.Time) error {
	p.Status = domain.PaymentPaid
	p.PaidAt = &now
	if err := s.repo.UpdatePayment(ctx, p); err != nil {
		return fmt.Errorf("billing: update payment: %w", err)
	}
	inv, err := s.repo.GetInvoice(ctx, p.InvoiceID)
	if err != nil {
		return fmt.Errorf("billing: get invoice: %w", err)
	}
	if inv.Status != domain.InvoicePaid {
		inv.Status = domain.InvoicePaid
		inv.PaidAt = &now
		if err := s.repo.UpdateInvoice(ctx, inv); err != nil {
			return fmt.Errorf("billing: update invoice: %w", err)
		}
	}
	s.log.Info().Str("orgId", p.OrgID.String()).Str("invoice", inv.Number).Str("provider", p.Provider).Msg("invoice paid")

	oldest, _, err := s.openInvoices(ctx, inv.OrgID)
	if err != nil {
		return fmt.Errorf("billing: %w", err)
	}
	sub, err := s.getSub(ctx, inv.OrgID)
	if err != nil {
		return err
	}
	if sub == nil {
		return nil
	}
	if oldest == nil && (sub.Status == domain.SubPastDue || sub.Status == domain.SubTrialing) {
		sub.Status = domain.SubActive
		sub.TrialEndsAt = nil
		sub.UpdatedAt = now
		if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
			return fmt.Errorf("billing: activate subscription: %w", err)
		}
	}
	if err := s.syncOrg(ctx, inv.OrgID, func(o *domain.Organization) bool {
		changed := false
		if oldest == nil && o.Status == domain.OrgSuspended {
			o.Status = domain.OrgActive
			changed = true
		}
		if o.PlanCode != sub.PlanCode {
			o.PlanCode = sub.PlanCode
			changed = true
		}
		return changed
	}); err != nil {
		return err
	}
	s.publishUpdated(ctx, sub)
	return nil
}
