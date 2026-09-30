// Package memory is an in-memory domain.BillingRepository and org store for
// tests and local development. It mirrors the semantics the Postgres
// repository must provide:
//   - GetSubscription / GetInvoice / GetPayment / GetInvoiceForPeriod return
//     domain.ErrNotFound when absent; GetInvoiceForPeriod ignores void invoices;
//   - ListUsage with kind "" lists every kind, ordered by At;
//   - CreateInvoice assigns Number "CG-<year>-<seq:06>";
//   - HasUsageForCall reports whether any usage row references the call.
package memory

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Store keeps billing state in memory. It is safe for concurrent use.
type Store struct {
	mu          sync.Mutex
	subs        map[uuid.UUID]domain.Subscription // by org
	usage       []domain.UsageRecord
	invoices    map[uuid.UUID]domain.Invoice
	payments    map[uuid.UUID]domain.Payment
	orgs        map[uuid.UUID]domain.Organization
	activeCalls map[uuid.UUID]int
	seq         int
}

// New returns an empty store.
func New() *Store {
	return &Store{
		subs: map[uuid.UUID]domain.Subscription{}, invoices: map[uuid.UUID]domain.Invoice{},
		payments: map[uuid.UUID]domain.Payment{}, orgs: map[uuid.UUID]domain.Organization{},
		activeCalls: map[uuid.UUID]int{},
	}
}

var _ domain.BillingRepository = (*Store)(nil)

func notFound(what string) error { return fmt.Errorf("%s: %w", what, domain.ErrNotFound) }

func cloneSub(s domain.Subscription) domain.Subscription {
	if s.TrialEndsAt != nil {
		t := *s.TrialEndsAt
		s.TrialEndsAt = &t
	}
	if s.CanceledAt != nil {
		t := *s.CanceledAt
		s.CanceledAt = &t
	}
	if s.CustomLimits != nil {
		c := *s.CustomLimits
		c.Features = slices.Clone(c.Features)
		s.CustomLimits = &c
	}
	return s
}

func cloneInvoice(inv domain.Invoice) domain.Invoice {
	inv.Lines = slices.Clone(inv.Lines)
	if inv.PaidAt != nil {
		t := *inv.PaidAt
		inv.PaidAt = &t
	}
	return inv
}

func clonePayment(p domain.Payment) domain.Payment {
	p.DeepLinks = slices.Clone(p.DeepLinks)
	p.Raw = maps.Clone(p.Raw)
	if p.PaidAt != nil {
		t := *p.PaidAt
		p.PaidAt = &t
	}
	if p.ExpiresAt != nil {
		t := *p.ExpiresAt
		p.ExpiresAt = &t
	}
	return p
}

func cloneOrg(o domain.Organization) domain.Organization {
	o.Settings = maps.Clone(o.Settings)
	return o
}

// ---- orgs (billing.OrgStore) ----

// PutOrg inserts or replaces an organisation.
func (s *Store) PutOrg(o domain.Organization) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgs[o.ID] = cloneOrg(o)
}

// GetOrg implements billing.OrgStore.
func (s *Store) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[id]
	if !ok {
		return nil, notFound("org")
	}
	c := cloneOrg(o)
	return &c, nil
}

// UpdateOrg implements billing.OrgStore.
func (s *Store) UpdateOrg(_ context.Context, o *domain.Organization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[o.ID]; !ok {
		return notFound("org")
	}
	s.orgs[o.ID] = cloneOrg(*o)
	return nil
}

// ---- subscriptions ----

// UpsertSubscription implements domain.BillingRepository (one per org).
func (s *Store) UpsertSubscription(_ context.Context, sub *domain.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub.ID == uuid.Nil {
		sub.ID = uuid.New()
	}
	s.subs[sub.OrgID] = cloneSub(*sub)
	return nil
}

// GetSubscription implements domain.BillingRepository.
func (s *Store) GetSubscription(_ context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subs[orgID]
	if !ok {
		return nil, notFound("subscription")
	}
	c := cloneSub(sub)
	return &c, nil
}

// ListSubscriptions implements domain.BillingRepository ("" = all).
func (s *Store) ListSubscriptions(_ context.Context, status domain.SubscriptionStatus) ([]domain.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Subscription
	for _, sub := range s.subs {
		if status == "" || sub.Status == status {
			out = append(out, cloneSub(sub))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ---- usage ----

// AddUsage implements domain.BillingRepository.
func (s *Store) AddUsage(_ context.Context, recs []domain.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		if r.ID == uuid.Nil {
			r.ID = uuid.New()
		}
		s.usage = append(s.usage, r)
	}
	return nil
}

func inRange(t, from, to time.Time) bool { return !t.Before(from) && t.Before(to) }

// SummarizeUsage implements domain.BillingRepository for [from, to). The
// included/overage fields are left zero (the service fills them).
func (s *Store) SummarizeUsage(_ context.Context, orgID uuid.UUID, from, to time.Time) (domain.UsageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := domain.UsageSummary{OrgID: orgID, PeriodStart: from, PeriodEnd: to}
	for _, r := range s.usage {
		if r.OrgID != orgID || !inRange(r.At, from, to) {
			continue
		}
		sum.CostMNT += r.CostMNT
		switch r.Kind {
		case domain.UsageCallMinutes:
			sum.Calls++
			sum.Minutes += r.Quantity
		case domain.UsageLLMTokensIn, domain.UsageLLMTokensOut:
			sum.LLMTokens += int64(r.Quantity)
		case domain.UsageSTTSeconds:
			sum.STTSeconds += r.Quantity
		case domain.UsageTTSChars:
			sum.TTSChars += int64(r.Quantity)
		case domain.UsageSMS:
			sum.SMS += int(r.Quantity)
		}
	}
	return sum, nil
}

// ListUsage implements domain.BillingRepository (kind "" = all kinds,
// limit <= 0 = no limit).
func (s *Store) ListUsage(_ context.Context, orgID uuid.UUID, from, to time.Time, kind domain.UsageKind, limit, offset int) ([]domain.UsageRecord, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []domain.UsageRecord
	for _, r := range s.usage {
		if r.OrgID == orgID && inRange(r.At, from, to) && (kind == "" || r.Kind == kind) {
			all = append(all, r)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	total := len(all)
	if offset > total {
		offset = total
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}

// HasUsageForCall implements billing.UsageChecker.
func (s *Store) HasUsageForCall(_ context.Context, callID uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.usage {
		if r.CallID != nil && *r.CallID == callID {
			return true, nil
		}
	}
	return false, nil
}

// SetActiveCalls sets what CountActiveCalls returns for an org.
func (s *Store) SetActiveCalls(orgID uuid.UUID, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeCalls[orgID] = n
}

// CountActiveCalls implements domain.BillingRepository.
func (s *Store) CountActiveCalls(_ context.Context, orgID uuid.UUID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeCalls[orgID], nil
}

// ---- invoices ----

// CreateInvoice implements domain.BillingRepository; assigns Number.
func (s *Store) CreateInvoice(_ context.Context, inv *domain.Invoice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inv.ID == uuid.Nil {
		inv.ID = uuid.New()
	}
	if _, ok := s.invoices[inv.ID]; ok {
		return fmt.Errorf("invoice: %w", domain.ErrConflict)
	}
	s.seq++
	year := inv.CreatedAt.Year()
	if inv.CreatedAt.IsZero() {
		year = time.Now().Year()
	}
	inv.Number = fmt.Sprintf("CG-%d-%06d", year, s.seq)
	s.invoices[inv.ID] = cloneInvoice(*inv)
	return nil
}

// UpdateInvoice implements domain.BillingRepository.
func (s *Store) UpdateInvoice(_ context.Context, inv *domain.Invoice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.invoices[inv.ID]; !ok {
		return notFound("invoice")
	}
	s.invoices[inv.ID] = cloneInvoice(*inv)
	return nil
}

// GetInvoice implements domain.BillingRepository.
func (s *Store) GetInvoice(_ context.Context, id uuid.UUID) (*domain.Invoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invoices[id]
	if !ok {
		return nil, notFound("invoice")
	}
	c := cloneInvoice(inv)
	return &c, nil
}

// ListInvoices implements domain.BillingRepository (newest first).
func (s *Store) ListInvoices(_ context.Context, orgID uuid.UUID) ([]domain.Invoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Invoice
	for _, inv := range s.invoices {
		if inv.OrgID == orgID {
			out = append(out, cloneInvoice(inv))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

// GetInvoiceForPeriod implements domain.BillingRepository (void ignored).
func (s *Store) GetInvoiceForPeriod(_ context.Context, orgID uuid.UUID, periodStart time.Time) (*domain.Invoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inv := range s.invoices {
		if inv.OrgID == orgID && inv.PeriodStart.Equal(periodStart) && inv.Status != domain.InvoiceVoid {
			c := cloneInvoice(inv)
			return &c, nil
		}
	}
	return nil, notFound("invoice")
}

// ---- payments ----

// CreatePayment implements domain.BillingRepository.
func (s *Store) CreatePayment(_ context.Context, p *domain.Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	if _, ok := s.payments[p.ID]; ok {
		return fmt.Errorf("payment: %w", domain.ErrConflict)
	}
	s.payments[p.ID] = clonePayment(*p)
	return nil
}

// UpdatePayment implements domain.BillingRepository.
func (s *Store) UpdatePayment(_ context.Context, p *domain.Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.payments[p.ID]; !ok {
		return notFound("payment")
	}
	s.payments[p.ID] = clonePayment(*p)
	return nil
}

// GetPayment implements domain.BillingRepository.
func (s *Store) GetPayment(_ context.Context, id uuid.UUID) (*domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return nil, notFound("payment")
	}
	c := clonePayment(p)
	return &c, nil
}

// GetPaymentByProviderRef implements domain.BillingRepository.
func (s *Store) GetPaymentByProviderRef(_ context.Context, provider, ref string) (*domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.payments {
		if p.Provider == provider && p.ProviderRef == ref {
			c := clonePayment(p)
			return &c, nil
		}
	}
	return nil, notFound("payment")
}

// ListPayments implements domain.BillingRepository (oldest first).
func (s *Store) ListPayments(_ context.Context, invoiceID uuid.UUID) ([]domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Payment
	for _, p := range s.payments {
		if p.InvoiceID == invoiceID {
			out = append(out, clonePayment(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
