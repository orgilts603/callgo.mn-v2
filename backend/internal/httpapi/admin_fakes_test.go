package httpapi

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// admIdentity embeds the interface so only the methods admin uses are
// implemented; any other call panics (a test bug).
type admIdentity struct {
	domain.IdentityRepository
	mu     sync.Mutex
	orgs   map[uuid.UUID]*domain.Organization
	counts map[uuid.UUID]int
	// pageCalls records ListOrgs calls (limit, offset).
	pageCalls [][2]int
}

func newAdmIdentity() *admIdentity {
	return &admIdentity{orgs: map[uuid.UUID]*domain.Organization{}, counts: map[uuid.UUID]int{}}
}

func (f *admIdentity) sorted() []domain.Organization {
	out := make([]domain.Organization, 0, len(f.orgs))
	for _, o := range f.orgs {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *admIdentity) ListOrgs(_ context.Context, search string, limit, offset int) ([]domain.Organization, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageCalls = append(f.pageCalls, [2]int{limit, offset})
	var all []domain.Organization
	for _, o := range f.sorted() {
		if search == "" || containsFold(o.Name, search) || containsFold(o.Slug, search) {
			all = append(all, o)
		}
	}
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := min(offset+limit, total)
	return all[offset:end], total, nil
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	ls, lsub := []rune(s), []rune(sub)
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(lsub) <= len(ls); i++ {
		ok := true
		for j := range lsub {
			if lower(ls[i+j]) != lower(lsub[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func (f *admIdentity) UpdateOrg(_ context.Context, o *domain.Organization) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.orgs[o.ID]; !ok {
		return domain.ErrNotFound
	}
	c := *o
	f.orgs[o.ID] = &c
	return nil
}

func (f *admIdentity) CountUsers(_ context.Context, orgID uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[orgID], nil
}

// admUsers implements domain.OrgRepository for orgs and users.
type admUsers struct {
	domain.OrgRepository
	id    *admIdentity
	users map[uuid.UUID]domain.User
}

func (f *admUsers) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	f.id.mu.Lock()
	defer f.id.mu.Unlock()
	o, ok := f.id.orgs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *o
	return &c, nil
}

func (f *admUsers) GetUser(_ context.Context, id uuid.UUID) (*domain.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &u, nil
}

func (f *admUsers) ListUsers(_ context.Context, orgID uuid.UUID) ([]domain.User, error) {
	var out []domain.User
	for _, u := range f.users {
		if u.OrgID == orgID {
			out = append(out, u)
		}
	}
	return out, nil
}

// admBilling implements domain.BillingRepository for the admin endpoints.
type admBilling struct {
	domain.BillingRepository
	mu       sync.Mutex
	subs     map[uuid.UUID]*domain.Subscription
	usage    map[uuid.UUID]domain.UsageSummary
	invoices map[uuid.UUID][]domain.Invoice
	// windows records the SummarizeUsage windows requested.
	windows [][2]time.Time
}

func newAdmBilling() *admBilling {
	return &admBilling{
		subs:     map[uuid.UUID]*domain.Subscription{},
		usage:    map[uuid.UUID]domain.UsageSummary{},
		invoices: map[uuid.UUID][]domain.Invoice{},
	}
}

func (f *admBilling) GetSubscription(_ context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subs[orgID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *s
	return &c, nil
}

func (f *admBilling) ListSubscriptions(_ context.Context, status domain.SubscriptionStatus) ([]domain.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Subscription
	for _, s := range f.subs {
		if status == "" || s.Status == status {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (f *admBilling) SummarizeUsage(_ context.Context, orgID uuid.UUID, from, to time.Time) (domain.UsageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, [2]time.Time{from, to})
	u := f.usage[orgID]
	u.OrgID, u.PeriodStart, u.PeriodEnd = orgID, from, to
	return u, nil
}

func (f *admBilling) ListInvoices(_ context.Context, orgID uuid.UUID) ([]domain.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invoices[orgID], nil
}

type admAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *admAudit) Append(_ context.Context, e *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *e)
	return nil
}
