package campaign

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// ---------------------------------------------------------------------------
// clock

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)}
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

// ---------------------------------------------------------------------------
// campaign repository

type fakeCampaignRepo struct {
	mu        sync.Mutex
	clock     func() time.Time
	campaigns map[uuid.UUID]*domain.Campaign
	order     []uuid.UUID
	targets   map[uuid.UUID][]*domain.CampaignTarget
	claims    int
}

func newFakeCampaignRepo(clock func() time.Time) *fakeCampaignRepo {
	return &fakeCampaignRepo{
		clock:     clock,
		campaigns: map[uuid.UUID]*domain.Campaign{},
		targets:   map[uuid.UUID][]*domain.CampaignTarget{},
	}
}

func (r *fakeCampaignRepo) CreateCampaign(_ context.Context, c *domain.Campaign, targets []domain.CampaignTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.campaigns[c.ID] = &cp
	r.order = append(r.order, c.ID)
	for i := range targets {
		t := targets[i]
		r.targets[c.ID] = append(r.targets[c.ID], &t)
	}
	return nil
}

func (r *fakeCampaignRepo) UpdateCampaign(_ context.Context, c *domain.Campaign) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.campaigns[c.ID]; !ok {
		return domain.ErrNotFound
	}
	cp := *c
	r.campaigns[c.ID] = &cp
	return nil
}

func (r *fakeCampaignRepo) GetCampaign(_ context.Context, id uuid.UUID) (*domain.Campaign, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.campaigns[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *fakeCampaignRepo) ListCampaigns(_ context.Context, orgID uuid.UUID) ([]domain.Campaign, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Campaign
	for _, id := range r.order {
		if c := r.campaigns[id]; c.OrgID == orgID {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (r *fakeCampaignRepo) ListRunningCampaigns(_ context.Context) ([]domain.Campaign, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Campaign
	for _, id := range r.order {
		if c := r.campaigns[id]; c.Status == domain.CampaignRunning {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (r *fakeCampaignRepo) ListTargets(_ context.Context, campaignID uuid.UUID, limit, offset int) ([]domain.CampaignTarget, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.targets[campaignID]
	var out []domain.CampaignTarget
	for i := offset; i < len(all) && len(out) < limit; i++ {
		out = append(out, *all[i])
	}
	return out, len(all), nil
}

func (r *fakeCampaignRepo) ClaimTargets(_ context.Context, campaignID uuid.UUID, n int) ([]domain.CampaignTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims++
	now := r.clock()
	var out []domain.CampaignTarget
	for _, t := range r.targets[campaignID] {
		if len(out) >= n {
			break
		}
		if t.Status != domain.TargetPending || (t.NextTryAt != nil && t.NextTryAt.After(now)) {
			continue
		}
		t.Status = domain.TargetCalling
		t.UpdatedAt = now
		out = append(out, *t)
	}
	return out, nil
}

func (r *fakeCampaignRepo) UpdateTarget(_ context.Context, t *domain.CampaignTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, cur := range r.targets[t.CampaignID] {
		if cur.ID == t.ID {
			cp := *t
			r.targets[t.CampaignID][i] = &cp
			return nil
		}
	}
	return domain.ErrNotFound
}

func (r *fakeCampaignRepo) CountActiveTargets(_ context.Context, campaignID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.targets[campaignID] {
		if t.Status == domain.TargetCalling {
			n++
		}
	}
	return n, nil
}

func (r *fakeCampaignRepo) target(campaignID, id uuid.UUID) domain.CampaignTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.targets[campaignID] {
		if t.ID == id {
			return *t
		}
	}
	panic("target not found")
}

func (r *fakeCampaignRepo) allTargets(campaignID uuid.UUID) []domain.CampaignTarget {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.CampaignTarget, 0, len(r.targets[campaignID]))
	for _, t := range r.targets[campaignID] {
		out = append(out, *t)
	}
	return out
}

func (r *fakeCampaignRepo) claimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claims
}

// ---------------------------------------------------------------------------
// call repository

type fakeCallRepo struct {
	mu    sync.Mutex
	calls map[uuid.UUID]*domain.Call
	order []uuid.UUID
}

func newFakeCallRepo() *fakeCallRepo { return &fakeCallRepo{calls: map[uuid.UUID]*domain.Call{}} }

func (r *fakeCallRepo) CreateCall(_ context.Context, c *domain.Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.calls[c.ID]; ok {
		return domain.ErrConflict
	}
	cp := *c
	r.calls[c.ID] = &cp
	r.order = append(r.order, c.ID)
	return nil
}

func (r *fakeCallRepo) UpdateCall(_ context.Context, c *domain.Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.calls[c.ID]; !ok {
		return domain.ErrNotFound
	}
	cp := *c
	r.calls[c.ID] = &cp
	return nil
}

func (r *fakeCallRepo) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *fakeCallRepo) GetCallByRoom(_ context.Context, room string) (*domain.Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.RoomName == room {
			cp := *c
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *fakeCallRepo) ListCalls(context.Context, domain.CallFilter) ([]domain.Call, int, error) {
	return nil, 0, nil
}
func (r *fakeCallRepo) ListActiveCalls(context.Context, uuid.UUID) ([]domain.Call, error) {
	return nil, nil
}
func (r *fakeCallRepo) AddTurn(context.Context, *domain.TranscriptTurn) error { return nil }
func (r *fakeCallRepo) UpdateTurnText(context.Context, uuid.UUID, string) error {
	return nil
}
func (r *fakeCallRepo) GetTurn(context.Context, uuid.UUID) (*domain.TranscriptTurn, error) {
	return nil, domain.ErrNotFound
}
func (r *fakeCallRepo) ListTurns(context.Context, uuid.UUID) ([]domain.TranscriptTurn, error) {
	return nil, nil
}
func (r *fakeCallRepo) Stats(context.Context, uuid.UUID) (domain.CallStats, error) {
	return domain.CallStats{}, nil
}
func (r *fakeCallRepo) DailySeries(context.Context, uuid.UUID, int) ([]domain.DailyCallCount, error) {
	return nil, nil
}

func (r *fakeCallRepo) all() []domain.Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Call, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.calls[id])
	}
	return out
}

// ---------------------------------------------------------------------------
// contacts, SIP numbers, profiles

type fakeContactRepo struct {
	mu      sync.Mutex
	byPhone map[string]*domain.Contact
}

func newFakeContactRepo() *fakeContactRepo {
	return &fakeContactRepo{byPhone: map[string]*domain.Contact{}}
}

func (r *fakeContactRepo) UpsertContact(_ context.Context, c *domain.Contact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := c.OrgID.String() + c.Phone
	if cur, ok := r.byPhone[key]; ok {
		c.ID = cur.ID
	}
	cp := *c
	r.byPhone[key] = &cp
	return nil
}

func (r *fakeContactRepo) GetContact(_ context.Context, id uuid.UUID) (*domain.Contact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.byPhone {
		if c.ID == id {
			cp := *c
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *fakeContactRepo) GetContactByPhone(_ context.Context, orgID uuid.UUID, phone string) (*domain.Contact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byPhone[orgID.String()+phone]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *fakeContactRepo) ListContacts(context.Context, uuid.UUID, string, int, int) ([]domain.Contact, int, error) {
	return nil, 0, nil
}
func (r *fakeContactRepo) DeleteContact(context.Context, uuid.UUID) error { return nil }

func (r *fakeContactRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byPhone)
}

type fakeSIPRepo struct {
	mu      sync.Mutex
	numbers map[uuid.UUID]domain.SIPNumber
}

func (r *fakeSIPRepo) CreateSIPNumber(_ context.Context, n *domain.SIPNumber) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.numbers[n.ID] = *n
	return nil
}
func (r *fakeSIPRepo) UpdateSIPNumber(ctx context.Context, n *domain.SIPNumber) error {
	return r.CreateSIPNumber(ctx, n)
}
func (r *fakeSIPRepo) DeleteSIPNumber(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.numbers, id)
	return nil
}
func (r *fakeSIPRepo) GetSIPNumber(_ context.Context, id uuid.UUID) (*domain.SIPNumber, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.numbers[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &n, nil
}
func (r *fakeSIPRepo) GetSIPNumberByNumber(context.Context, string) (*domain.SIPNumber, error) {
	return nil, domain.ErrNotFound
}
func (r *fakeSIPRepo) ListSIPNumbers(context.Context, uuid.UUID) ([]domain.SIPNumber, error) {
	return nil, nil
}

type fakeProfileRepo struct {
	mu       sync.Mutex
	profiles map[uuid.UUID]domain.AgentProfile
}

func (r *fakeProfileRepo) CreateAgentProfile(_ context.Context, p *domain.AgentProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[p.ID] = *p
	return nil
}
func (r *fakeProfileRepo) UpdateAgentProfile(ctx context.Context, p *domain.AgentProfile) error {
	return r.CreateAgentProfile(ctx, p)
}
func (r *fakeProfileRepo) DeleteAgentProfile(context.Context, uuid.UUID) error { return nil }
func (r *fakeProfileRepo) GetAgentProfile(_ context.Context, id uuid.UUID) (*domain.AgentProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.profiles[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &p, nil
}
func (r *fakeProfileRepo) ListAgentProfiles(context.Context, uuid.UUID) ([]domain.AgentProfile, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// event bus

type fakeBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *fakeBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *fakeBus) all() []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Event(nil), b.events...)
}

// typesFor returns the event types published for one call, in order.
func (b *fakeBus) typesFor(callID uuid.UUID) []domain.EventType {
	var out []domain.EventType
	for _, ev := range b.all() {
		if ev.CallID != nil && *ev.CallID == callID {
			out = append(out, ev.Type)
		}
	}
	return out
}

func (b *fakeBus) count(typ domain.EventType) int {
	n := 0
	for _, ev := range b.all() {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// telephony

type dialScript func(req domain.OutboundCallRequest) (domain.OutboundCallResult, error)

func answered(domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	return domain.OutboundCallResult{Answered: true, ParticipantID: "PA_1", SIPCallID: "SCL_1"}, nil
}

func noAnswer(domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	return domain.OutboundCallResult{}, fmt.Errorf("twirp error: sip request timed out")
}

// fakeTelephony is a scripted Telephony. When gate is non-nil each Dial blocks
// until a value is received from gate (or ctx ends).
type fakeTelephony struct {
	mu       sync.Mutex
	script   dialScript
	gate     chan struct{}
	requests []domain.OutboundCallRequest

	inflight    atomic.Int32
	maxInflight atomic.Int32
}

func (f *fakeTelephony) setScript(s dialScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = s
}

func (f *fakeTelephony) Dial(ctx context.Context, req domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	script, gate := f.script, f.gate
	f.mu.Unlock()

	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return domain.OutboundCallResult{}, ctx.Err()
		}
	}
	if script == nil {
		return answered(req)
	}
	return script(req)
}

func (f *fakeTelephony) dials() []domain.OutboundCallRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.OutboundCallRequest(nil), f.requests...)
}

func (f *fakeTelephony) EnsureNumberProvisioned(context.Context, *domain.SIPNumber) error {
	return nil
}
func (f *fakeTelephony) DeprovisionNumber(context.Context, *domain.SIPNumber) error { return nil }
func (f *fakeTelephony) Hangup(context.Context, string) error                       { return nil }
func (f *fakeTelephony) TransferCall(context.Context, string, string, string) error {
	return nil
}
func (f *fakeTelephony) ListActiveRooms(context.Context) ([]string, error) { return nil, nil }
