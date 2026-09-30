package callbacks

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type harness struct {
	t        *testing.T
	clock    *fakeClock
	repo     *fakeIntegrations
	calls    *fakeCalls
	contacts *fakeContacts
	numbers  *fakeNumbers
	profiles *fakeProfiles
	tel      *fakeTel
	ent      *fakeEnt
	bus      *fakeBus
	s        *Scheduler

	org     uuid.UUID
	number  domain.SIPNumber
	profile domain.AgentProfile
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := newFakeClock()
	h := &harness{
		t: t, clock: clock, repo: newFakeIntegrations(clock.Now), calls: newFakeCalls(), contacts: newFakeContacts(),
		numbers: &fakeNumbers{}, profiles: &fakeProfiles{}, tel: &fakeTel{result: domain.OutboundCallResult{ParticipantID: "PA_1", SIPCallID: "SCL_1"}},
		ent: &fakeEnt{ok: true}, bus: &fakeBus{}, org: uuid.New(),
	}
	profID := uuid.New()
	h.profile = domain.AgentProfile{ID: profID, OrgID: h.org, Name: "Sales"}
	h.profiles.items = []domain.AgentProfile{h.profile}
	h.number = domain.SIPNumber{ID: uuid.New(), OrgID: h.org, Number: "+97677001234", Active: true, AllowOutbound: true, AgentProfileID: &profID}
	h.numbers.items = []domain.SIPNumber{h.number}
	h.rebuild()
	return h
}

func (h *harness) rebuild() {
	h.s = New(h.repo, h.calls, h.contacts, h.numbers, h.profiles, h.tel, h.ent, h.bus,
		Config{Now: h.clock.Now}, zerolog.Nop())
}

func (h *harness) create(mut ...func(*domain.CallbackRequest)) domain.CallbackRequest {
	h.t.Helper()
	c := &domain.CallbackRequest{OrgID: h.org, Phone: "99112233", Name: "Bat", Note: "asked about price", DueAt: h.clock.Now().Add(time.Minute)}
	for _, m := range mut {
		m(c)
	}
	require.NoError(h.t, h.s.Create(context.Background(), c))
	return *c
}

func (h *harness) poll() int { return h.s.Poll(context.Background()) }

func TestCreate(t *testing.T) {
	h := newHarness(t)
	ct := domain.Contact{ID: uuid.New(), OrgID: h.org, Phone: "+97699112233", Name: "Batbayar"}
	h.contacts.add(ct)

	c := h.create(func(c *domain.CallbackRequest) { c.Name = "" })
	assert.Equal(t, "+97699112233", c.Phone, "normalised to E.164")
	assert.Equal(t, domain.CallbackPending, c.Status)
	assert.NotEqual(t, uuid.Nil, c.ID)
	require.NotNil(t, c.ContactID)
	assert.Equal(t, ct.ID, *c.ContactID)
	assert.Equal(t, "Batbayar", c.Name, "name filled from the contact")
	stored := h.repo.get(c.ID)
	assert.Equal(t, c.Phone, stored.Phone)
	assert.Equal(t, []domain.EventType{EventCallbackScheduled}, h.bus.types())
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other := uuid.New()
	tests := []struct {
		name string
		mut  func(*domain.CallbackRequest)
	}{
		{"bad phone", func(c *domain.CallbackRequest) { c.Phone = "abc" }},
		{"empty phone", func(c *domain.CallbackRequest) { c.Phone = "" }},
		{"due in the past", func(c *domain.CallbackRequest) { c.DueAt = h.clock.Now().Add(-time.Second) }},
		{"due now", func(c *domain.CallbackRequest) { c.DueAt = h.clock.Now() }},
		{"no org", func(c *domain.CallbackRequest) { c.OrgID = uuid.Nil }},
		{"long note", func(c *domain.CallbackRequest) { c.Note = string(make([]byte, maxNoteLen+1)) }},
		{"unknown number", func(c *domain.CallbackRequest) { c.SIPNumberID = &other }},
		{"unknown profile", func(c *domain.CallbackRequest) { c.AgentProfileID = &other }},
		{"unknown contact", func(c *domain.CallbackRequest) { c.ContactID = &other }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &domain.CallbackRequest{OrgID: h.org, Phone: "99112233", DueAt: h.clock.Now().Add(time.Hour)}
			tc.mut(c)
			err := h.s.Create(ctx, c)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalid)
		})
	}
	assert.Empty(t, h.bus.types())

	foreign := domain.SIPNumber{ID: uuid.New(), OrgID: uuid.New(), Active: true}
	h.numbers.items = append(h.numbers.items, foreign)
	err := h.s.Create(ctx, &domain.CallbackRequest{OrgID: h.org, Phone: "99112233", DueAt: h.clock.Now().Add(time.Hour), SIPNumberID: &foreign.ID})
	assert.ErrorIs(t, err, domain.ErrInvalid, "other org's number is rejected")
}

func TestCreateCallbackImplementsCreator(t *testing.T) {
	h := newHarness(t)
	var creator CallbackCreator = h.s
	c := &domain.CallbackRequest{OrgID: h.org, Phone: "+97699112233", DueAt: h.clock.Now().Add(time.Hour)}
	require.NoError(t, creator.CreateCallback(context.Background(), c))
	assert.Equal(t, domain.CallbackPending, h.repo.get(c.ID).Status)
}

func TestPollNotDueYet(t *testing.T) {
	h := newHarness(t)
	c := h.create()
	assert.Equal(t, 0, h.poll())
	assert.Equal(t, domain.CallbackPending, h.repo.get(c.ID).Status)
	assert.Empty(t, h.tel.dialed())
}

func TestPollDialsDueCallback(t *testing.T) {
	h := newHarness(t)
	ct := domain.Contact{ID: uuid.New(), OrgID: h.org, Phone: "+97699112233", Name: "Bat"}
	h.contacts.add(ct)
	c := h.create()
	h.clock.Advance(2 * time.Minute)

	require.Equal(t, 1, h.poll())

	got := h.repo.get(c.ID)
	assert.Equal(t, domain.CallbackDialed, got.Status)
	assert.Equal(t, 1, got.Attempts)
	require.NotNil(t, got.ResultCallID)

	calls := h.calls.all()
	require.Len(t, calls, 1)
	call := calls[0]
	assert.Equal(t, *got.ResultCallID, call.ID)
	assert.Equal(t, domain.DirectionOutbound, call.Direction)
	assert.Equal(t, "call-"+call.ID.String(), call.RoomName)
	assert.Equal(t, "+97677001234", call.FromNumber)
	assert.Equal(t, "+97699112233", call.ToNumber)
	assert.Equal(t, c.ID.String(), call.Metadata["callbackId"])
	assert.Equal(t, "asked about price", call.Metadata["note"])
	assert.Equal(t, "PA_1", call.ParticipantID)
	assert.Equal(t, "SCL_1", call.SIPCallID)
	require.NotNil(t, call.ContactID)
	assert.Equal(t, ct.ID, *call.ContactID)
	require.NotNil(t, call.AgentProfileID)
	assert.Equal(t, h.profile.ID, *call.AgentProfileID)

	reqs := h.tel.dialed()
	require.Len(t, reqs, 1)
	assert.False(t, reqs[0].WaitUntilAnswered)
	assert.Equal(t, call.ID, reqs[0].CallID)
	assert.Equal(t, "+97699112233", reqs[0].ToNumber)
	assert.Equal(t, c.ID.String(), reqs[0].Metadata["callbackId"])
	assert.Equal(t, h.profile.ID, reqs[0].AgentProfile.ID)

	types := h.bus.types()
	assert.Equal(t, []domain.EventType{EventCallbackScheduled, domain.EventCallStarted, domain.EventCallUpdated}, types)

	assert.Equal(t, 0, h.poll(), "a dialed callback is not claimed again")
}

func TestPollUsesGivenNumberAndProfile(t *testing.T) {
	h := newHarness(t)
	otherProfile := domain.AgentProfile{ID: uuid.New(), OrgID: h.org}
	h.profiles.items = append(h.profiles.items, otherProfile)
	otherNum := domain.SIPNumber{ID: uuid.New(), OrgID: h.org, Number: "+97677005555", Active: true, AllowOutbound: true}
	h.numbers.items = []domain.SIPNumber{h.number, otherNum}
	c := h.create(func(c *domain.CallbackRequest) { c.SIPNumberID, c.AgentProfileID = &otherNum.ID, &otherProfile.ID })
	h.clock.Advance(2 * time.Minute)
	require.Equal(t, 1, h.poll())
	reqs := h.tel.dialed()
	require.Len(t, reqs, 1)
	assert.Equal(t, otherNum.ID, reqs[0].FromNumber.ID)
	assert.Equal(t, otherProfile.ID, reqs[0].AgentProfile.ID)
	assert.Equal(t, domain.CallbackDialed, h.repo.get(c.ID).Status)
}

func TestPickFirstOutboundNumber(t *testing.T) {
	h := newHarness(t)
	inboundOnly := domain.SIPNumber{ID: uuid.New(), OrgID: h.org, Number: "+97677000001", Active: true, AllowInbound: true}
	inactive := domain.SIPNumber{ID: uuid.New(), OrgID: h.org, Number: "+97677000002", AllowOutbound: true}
	foreign := domain.SIPNumber{ID: uuid.New(), OrgID: uuid.New(), Number: "+97677000003", Active: true, AllowOutbound: true}
	h.numbers.items = []domain.SIPNumber{foreign, inboundOnly, inactive, h.number}
	c := h.create(func(c *domain.CallbackRequest) { c.SIPNumberID = &inactive.ID })
	h.clock.Advance(2 * time.Minute)
	require.Equal(t, 1, h.poll())
	reqs := h.tel.dialed()
	require.Len(t, reqs, 1)
	assert.Equal(t, h.number.ID, reqs[0].FromNumber.ID, "unusable given number falls back to the first outbound one")
	assert.Equal(t, domain.CallbackDialed, h.repo.get(c.ID).Status)
}

func TestNoUsableNumberOrProfileFails(t *testing.T) {
	h := newHarness(t)
	h.numbers.items = nil
	c := h.create()
	h.clock.Advance(2 * time.Minute)
	require.Equal(t, 1, h.poll())
	assert.Equal(t, domain.CallbackFailed, h.repo.get(c.ID).Status)
	assert.Empty(t, h.tel.dialed())

	h2 := newHarness(t)
	h2.numbers.items[0].AgentProfileID = nil
	c2 := h2.create()
	h2.clock.Advance(2 * time.Minute)
	require.Equal(t, 1, h2.poll())
	assert.Equal(t, domain.CallbackFailed, h2.repo.get(c2.ID).Status, "no profile anywhere")
	assert.Empty(t, h2.calls.all())
}

func TestEntitlementDeferral(t *testing.T) {
	h := newHarness(t)
	h.ent.ok, h.ent.reason = false, "quota exceeded"
	c := h.create()
	h.clock.Advance(2 * time.Minute)
	now := h.clock.Now()

	require.Equal(t, 1, h.poll())
	got := h.repo.get(c.ID)
	assert.Equal(t, domain.CallbackPending, got.Status)
	assert.True(t, got.DueAt.Equal(now.Add(15*time.Minute)), "due pushed 15m, got %s", got.DueAt)
	assert.Zero(t, got.Attempts)
	assert.Empty(t, h.tel.dialed())
	assert.Empty(t, h.calls.all())

	assert.Equal(t, 0, h.poll(), "not due until the deferral passes")

	h.clock.Advance(16 * time.Minute)
	h.ent.ok = true
	require.Equal(t, 1, h.poll())
	assert.Equal(t, domain.CallbackDialed, h.repo.get(c.ID).Status)
	assert.Len(t, h.tel.dialed(), 1)
}

func TestEntitlementErrorDefers(t *testing.T) {
	h := newHarness(t)
	h.ent.err = errBoom
	c := h.create()
	h.clock.Advance(2 * time.Minute)
	require.Equal(t, 1, h.poll())
	got := h.repo.get(c.ID)
	assert.Equal(t, domain.CallbackPending, got.Status)
	assert.True(t, got.DueAt.After(h.clock.Now()))
}

func TestClosedBusinessHoursReschedulesToOpening(t *testing.T) {
	h := newHarness(t)
	h.numbers.items[0].Routing = domain.RoutingConfig{
		BusinessHours:     domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"},
		AfterHoursMessage: "closed",
	}
	// 12:00 UTC = 20:00 in Ulaanbaatar: closed until 09:00 next morning (01:00 UTC).
	h.clock.Advance(9 * time.Hour)
	c := h.create()
	h.clock.Advance(2 * time.Minute)

	require.Equal(t, 1, h.poll())
	got := h.repo.get(c.ID)
	assert.Equal(t, domain.CallbackPending, got.Status)
	assert.True(t, got.DueAt.Equal(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)), "due at next opening, got %s", got.DueAt)
	assert.Zero(t, got.Attempts)
	assert.Empty(t, h.tel.dialed())

	// Once the opening time has passed it is dialed.
	h.clock.Advance(14 * time.Hour) // 2026-10-01 02:02 UTC = 10:02 in Ulaanbaatar
	require.Equal(t, 1, h.poll())
	assert.Equal(t, domain.CallbackDialed, h.repo.get(c.ID).Status)
	assert.Len(t, h.tel.dialed(), 1)
}

func TestBusinessHoursUseOrgTimezone(t *testing.T) {
	h := newHarness(t)
	h.numbers.items[0].Routing = domain.RoutingConfig{
		BusinessHours:     domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"},
		AfterHoursMessage: "closed",
	}
	// 03:00 UTC: 11:00 in Ulaanbaatar (open) but 23:00 in New York (closed).
	h.s.WithOrgs(fakeOrgs{tz: "America/New_York"})
	c := h.create()
	h.clock.Advance(2 * time.Minute)
	require.Equal(t, 1, h.poll())
	assert.Equal(t, domain.CallbackPending, h.repo.get(c.ID).Status)
	assert.Empty(t, h.tel.dialed())
}

func TestDialFailureSettlesAttempt(t *testing.T) {
	h := newHarness(t)
	h.tel.err = errBoom
	c := h.create()
	h.clock.Advance(2 * time.Minute)
	now := h.clock.Now()

	require.Equal(t, 1, h.poll())
	got := h.repo.get(c.ID)
	assert.Equal(t, domain.CallbackPending, got.Status, "first failure retries")
	assert.Equal(t, 1, got.Attempts)
	assert.True(t, got.DueAt.Equal(now.Add(30*time.Minute)))
	calls := h.calls.all()
	require.Len(t, calls, 1)
	assert.Equal(t, domain.StatusFailed, calls[0].Status)
	assert.NotNil(t, calls[0].EndedAt)
	assert.Equal(t, "boom", calls[0].Metadata["dialError"])
	assert.Contains(t, h.bus.types(), domain.EventCallEnded)

	h.clock.Advance(31 * time.Minute)
	require.Equal(t, 1, h.poll())
	got = h.repo.get(c.ID)
	assert.Equal(t, domain.CallbackFailed, got.Status, "second failure is final")
	assert.Equal(t, 2, got.Attempts)
	assert.Len(t, h.tel.dialed(), 2)
}

func (h *harness) dialOne(c domain.CallbackRequest) domain.Call {
	h.t.Helper()
	require.Equal(h.t, 1, h.poll())
	calls := h.calls.all()
	return calls[len(calls)-1]
}

func TestOnCallEnded(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status domain.CallStatus
		want   domain.CallbackStatus
	}{
		{domain.StatusCompleted, domain.CallbackDone},
		{domain.StatusVoicemail, domain.CallbackDone},
		{domain.StatusNoAnswer, domain.CallbackPending},
		{domain.StatusBusy, domain.CallbackPending},
		{domain.StatusFailed, domain.CallbackPending},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			h := newHarness(t)
			c := h.create()
			h.clock.Advance(2 * time.Minute)
			call := h.dialOne(c)
			call.Status = tc.status
			now := h.clock.Now()

			h.s.OnCallEnded(ctx, &call)

			got := h.repo.get(c.ID)
			assert.Equal(t, tc.want, got.Status)
			if tc.want == domain.CallbackPending {
				assert.True(t, got.DueAt.Equal(now.Add(30*time.Minute)), "retry after 30m, got %s", got.DueAt)
			}
		})
	}
}

func TestRetryThenFailed(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	c := h.create()
	h.clock.Advance(2 * time.Minute)

	first := h.dialOne(c)
	first.Status = domain.StatusNoAnswer
	h.s.OnCallEnded(ctx, &first)
	require.Equal(t, domain.CallbackPending, h.repo.get(c.ID).Status)
	assert.Equal(t, 0, h.poll(), "waits for RetryAfter")

	h.clock.Advance(30 * time.Minute)
	second := h.dialOne(c)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, 2, h.repo.get(c.ID).Attempts)
	second.Status = domain.StatusBusy
	h.s.OnCallEnded(ctx, &second)
	assert.Equal(t, domain.CallbackFailed, h.repo.get(c.ID).Status)
	assert.Equal(t, 0, h.poll())
}

func TestOnCallEndedIgnores(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	c := h.create()
	h.clock.Advance(2 * time.Minute)
	call := h.dialOne(c)

	// Plain calls, malformed ids and non-terminal calls change nothing.
	h.s.OnCallEnded(ctx, nil)
	h.s.OnCallEnded(ctx, &domain.Call{ID: uuid.New(), Status: domain.StatusCompleted})
	h.s.OnCallEnded(ctx, &domain.Call{ID: uuid.New(), Status: domain.StatusCompleted, Metadata: map[string]any{"callbackId": "nope"}})
	h.s.OnCallEnded(ctx, &domain.Call{ID: uuid.New(), Status: domain.StatusCompleted, Metadata: map[string]any{"callbackId": uuid.NewString()}})
	active := call
	active.Status = domain.StatusActive
	h.s.OnCallEnded(ctx, &active)
	assert.Equal(t, domain.CallbackDialed, h.repo.get(c.ID).Status)

	// A stale call id of another attempt does not settle it.
	stale := call
	stale.ID = uuid.New()
	stale.Status = domain.StatusCompleted
	h.s.OnCallEnded(ctx, &stale)
	assert.Equal(t, domain.CallbackDialed, h.repo.get(c.ID).Status)

	// Duplicate end notifications are idempotent.
	call.Status = domain.StatusCompleted
	h.s.OnCallEnded(ctx, &call)
	require.Equal(t, domain.CallbackDone, h.repo.get(c.ID).Status)
	call.Status = domain.StatusNoAnswer
	h.s.OnCallEnded(ctx, &call)
	assert.Equal(t, domain.CallbackDone, h.repo.get(c.ID).Status)
}

func TestCanceledCallbackIsNotDialed(t *testing.T) {
	h := newHarness(t)
	c := h.create()
	stored := h.repo.get(c.ID)
	stored.Status = domain.CallbackCanceled
	require.NoError(t, h.repo.UpdateCallback(context.Background(), &stored))
	h.clock.Advance(time.Hour)
	assert.Equal(t, 0, h.poll())
	assert.Empty(t, h.tel.dialed())
}

func TestRunPollsUntilCancelled(t *testing.T) {
	h := newHarness(t)
	h.create()
	h.clock.Advance(2 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.s.Run(ctx) }()
	require.Eventually(t, func() bool { return len(h.tel.dialed()) == 1 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
