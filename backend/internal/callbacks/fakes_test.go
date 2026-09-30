package callbacks

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// 2026-09-30 is a Wednesday; 03:00 UTC is 11:00 in Ulaanbaatar.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeIntegrations implements the callback half of IntegrationsRepository.
type fakeIntegrations struct {
	domain.IntegrationsRepository
	mu    sync.Mutex
	clock func() time.Time
	items map[uuid.UUID]domain.CallbackRequest
	order []uuid.UUID
}

func newFakeIntegrations(clock func() time.Time) *fakeIntegrations {
	return &fakeIntegrations{clock: clock, items: map[uuid.UUID]domain.CallbackRequest{}}
}

func (f *fakeIntegrations) CreateCallback(_ context.Context, c *domain.CallbackRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[c.ID] = *c
	f.order = append(f.order, c.ID)
	return nil
}

func (f *fakeIntegrations) UpdateCallback(_ context.Context, c *domain.CallbackRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[c.ID]; !ok {
		return domain.ErrNotFound
	}
	f.items[c.ID] = *c
	return nil
}

func (f *fakeIntegrations) GetCallback(_ context.Context, id uuid.UUID) (*domain.CallbackRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *fakeIntegrations) ListCallbacks(_ context.Context, orgID uuid.UUID, status domain.CallbackStatus, limit, offset int) ([]domain.CallbackRequest, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.CallbackRequest
	for _, id := range f.order {
		c := f.items[id]
		if c.OrgID == orgID && (status == "" || c.Status == status) {
			out = append(out, c)
		}
	}
	total := len(out)
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

// ClaimDueCallbacks moves pending, due callbacks to dialed (Attempts is left
// alone, as the scheduler counts attempts itself).
func (f *fakeIntegrations) ClaimDueCallbacks(_ context.Context, n int) ([]domain.CallbackRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.CallbackRequest
	for _, id := range f.order {
		c := f.items[id]
		if len(out) >= n {
			break
		}
		if c.Status == domain.CallbackPending && !c.DueAt.After(f.clock()) {
			c.Status = domain.CallbackDialed
			f.items[id] = c
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeIntegrations) get(id uuid.UUID) domain.CallbackRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[id]
}

type fakeCalls struct {
	domain.CallRepository
	mu    sync.Mutex
	calls map[uuid.UUID]domain.Call
	order []uuid.UUID
}

func newFakeCalls() *fakeCalls { return &fakeCalls{calls: map[uuid.UUID]domain.Call{}} }

func (f *fakeCalls) CreateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[c.ID] = *c
	f.order = append(f.order, c.ID)
	return nil
}

func (f *fakeCalls) UpdateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[c.ID] = *c
	return nil
}

func (f *fakeCalls) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (f *fakeCalls) all() []domain.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Call
	for _, id := range f.order {
		out = append(out, f.calls[id])
	}
	return out
}

type fakeContacts struct {
	domain.ContactRepository
	byPhone map[string]domain.Contact
	byID    map[uuid.UUID]domain.Contact
}

func newFakeContacts() *fakeContacts {
	return &fakeContacts{byPhone: map[string]domain.Contact{}, byID: map[uuid.UUID]domain.Contact{}}
}

func (f *fakeContacts) add(c domain.Contact) {
	f.byPhone[c.Phone] = c
	f.byID[c.ID] = c
}

func (f *fakeContacts) GetContactByPhone(_ context.Context, orgID uuid.UUID, phone string) (*domain.Contact, error) {
	if c, ok := f.byPhone[phone]; ok && c.OrgID == orgID {
		return &c, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeContacts) GetContact(_ context.Context, id uuid.UUID) (*domain.Contact, error) {
	if c, ok := f.byID[id]; ok {
		return &c, nil
	}
	return nil, domain.ErrNotFound
}

type fakeNumbers struct {
	domain.SIPNumberRepository
	items []domain.SIPNumber
}

func (f *fakeNumbers) GetSIPNumber(_ context.Context, id uuid.UUID) (*domain.SIPNumber, error) {
	for _, n := range f.items {
		if n.ID == id {
			return &n, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeNumbers) ListSIPNumbers(_ context.Context, orgID uuid.UUID) ([]domain.SIPNumber, error) {
	var out []domain.SIPNumber
	for _, n := range f.items {
		if n.OrgID == orgID {
			out = append(out, n)
		}
	}
	return out, nil
}

type fakeProfiles struct {
	domain.AgentProfileRepository
	items []domain.AgentProfile
}

func (f *fakeProfiles) GetAgentProfile(_ context.Context, id uuid.UUID) (*domain.AgentProfile, error) {
	for _, p := range f.items {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeProfiles) ListAgentProfiles(_ context.Context, orgID uuid.UUID) ([]domain.AgentProfile, error) {
	var out []domain.AgentProfile
	for _, p := range f.items {
		if p.OrgID == orgID {
			out = append(out, p)
		}
	}
	return out, nil
}

type fakeOrgs struct {
	domain.OrgRepository
	tz string
}

func (f fakeOrgs) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	return &domain.Organization{ID: id, Timezone: f.tz}, nil
}

type fakeTel struct {
	domain.Telephony
	mu     sync.Mutex
	reqs   []domain.OutboundCallRequest
	result domain.OutboundCallResult
	err    error
}

func (f *fakeTel) Dial(_ context.Context, req domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return f.result, f.err
}

func (f *fakeTel) dialed() []domain.OutboundCallRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

type fakeEnt struct {
	mu     sync.Mutex
	ok     bool
	reason string
	err    error
	checks int
}

func (f *fakeEnt) CanStartCall(context.Context, uuid.UUID) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.ok, f.reason, f.err
}

func (f *fakeEnt) HasFeature(context.Context, uuid.UUID, string) (bool, error) { return true, nil }
func (f *fakeEnt) Limits(context.Context, uuid.UUID) (domain.Plan, error)      { return domain.Plan{}, nil }

type fakeBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *fakeBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *fakeBus) types() []domain.EventType {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []domain.EventType
	for _, e := range b.events {
		out = append(out, e.Type)
	}
	return out
}

var errBoom = errors.New("boom")
