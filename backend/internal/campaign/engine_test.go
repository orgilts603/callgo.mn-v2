package campaign

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type fixture struct {
	t         *testing.T
	clock     *fakeClock
	campaigns *fakeCampaignRepo
	calls     *fakeCallRepo
	contacts  *fakeContactRepo
	numbers   *fakeSIPRepo
	profiles  *fakeProfileRepo
	tel       *fakeTelephony
	bus       *fakeBus
	opts      Options
	engine    *Engine

	orgID   uuid.UUID
	sip     domain.SIPNumber
	profile domain.AgentProfile
}

func newFixture(t *testing.T, opts Options) *fixture {
	t.Helper()
	clock := newFakeClock()
	if opts.Clock == nil {
		opts.Clock = clock.Now
	}
	f := &fixture{
		t:         t,
		clock:     clock,
		campaigns: newFakeCampaignRepo(clock.Now),
		calls:     newFakeCallRepo(),
		contacts:  newFakeContactRepo(),
		numbers:   &fakeSIPRepo{numbers: map[uuid.UUID]domain.SIPNumber{}},
		profiles:  &fakeProfileRepo{profiles: map[uuid.UUID]domain.AgentProfile{}},
		tel:       &fakeTelephony{},
		bus:       &fakeBus{},
		opts:      opts,
		orgID:     uuid.New(),
	}
	f.profile = domain.AgentProfile{ID: uuid.New(), OrgID: f.orgID, Name: "Sales", Language: "mn"}
	f.sip = domain.SIPNumber{ID: uuid.New(), OrgID: f.orgID, Number: "+97677001234", AllowOutbound: true, Active: true}
	require.NoError(t, f.profiles.CreateAgentProfile(context.Background(), &f.profile))
	require.NoError(t, f.numbers.CreateSIPNumber(context.Background(), &f.sip))
	f.engine = f.newEngine()
	return f
}

func (f *fixture) newEngine() *Engine {
	return NewEngine(f.campaigns, f.calls, f.contacts, f.numbers, f.profiles, f.tel, f.bus, f.opts, zerolog.Nop())
}

// addCampaign creates a campaign with n targets in the given status.
func (f *fixture) addCampaign(status domain.CampaignStatus, concurrency, maxAttempts, n int) domain.Campaign {
	f.t.Helper()
	now := f.clock.Now()
	sipID, profileID := f.sip.ID, f.profile.ID
	c := domain.Campaign{
		ID:             uuid.New(),
		OrgID:          f.orgID,
		Name:           "Promo",
		SIPNumberID:    &sipID,
		AgentProfileID: &profileID,
		Status:         status,
		Concurrency:    concurrency,
		MaxAttempts:    maxAttempts,
		Total:          n,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	targets := make([]domain.CampaignTarget, n)
	for i := range targets {
		targets[i] = domain.CampaignTarget{
			ID:         uuid.New(),
			CampaignID: c.ID,
			Phone:      fmt.Sprintf("+9769900%04d", i),
			Name:       fmt.Sprintf("Customer %d", i),
			Vars:       map[string]string{"amount": fmt.Sprint(1000 * (i + 1))},
			Status:     domain.TargetPending,
			UpdatedAt:  now,
		}
	}
	require.NoError(f.t, f.campaigns.CreateCampaign(context.Background(), &c, targets))
	return c
}

// poll runs one dialer iteration and waits for its dial goroutines.
func (f *fixture) poll() {
	f.engine.pollOnce(context.Background())
	f.engine.wg.Wait()
}

func (f *fixture) campaign(id uuid.UUID) domain.Campaign {
	c, err := f.campaigns.GetCampaign(context.Background(), id)
	require.NoError(f.t, err)
	return *c
}

func (f *fixture) call(id uuid.UUID) domain.Call {
	c, err := f.calls.GetCall(context.Background(), id)
	require.NoError(f.t, err)
	return *c
}

// endCall simulates the HTTP layer persisting a terminal call and notifying
// the engine.
func (f *fixture) endCall(callID uuid.UUID, status domain.CallStatus, reason string) *domain.Call {
	f.t.Helper()
	c := f.call(callID)
	now := f.clock.Now()
	c.Status = status
	c.EndReason = reason
	c.EndedAt = &now
	c.DurationSec = 42
	require.NoError(f.t, f.calls.UpdateCall(context.Background(), &c))
	f.engine.OnCallEnded(context.Background(), &c)
	return &c
}

func TestCampaignConcurrencyLimitRespected(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.gate = make(chan struct{})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignRunning, 2, 1, 5)

	f.engine.pollOnce(context.Background())
	require.Eventually(t, func() bool { return len(f.tel.dials()) == 2 }, time.Second, time.Millisecond)

	// While both calls ring, further polls must not claim more targets.
	f.engine.pollOnce(context.Background())
	f.engine.pollOnce(context.Background())
	assert.Len(t, f.tel.dials(), 2)
	active, err := f.campaigns.CountActiveTargets(context.Background(), c.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, active)

	close(f.tel.gate)
	f.engine.wg.Wait()
	assert.EqualValues(t, 2, f.tel.maxInflight.Load())

	// Slots freed: the next poll dials the next two targets.
	f.poll()
	assert.Len(t, f.tel.dials(), 4)
	assert.LessOrEqual(t, f.tel.maxInflight.Load(), int32(2))
}

func TestGlobalConcurrencyLimitIncludesAnsweredCalls(t *testing.T) {
	f := newFixture(t, Options{MaxGlobalConcurrency: 3})
	f.tel.setScript(answered)
	a := f.addCampaign(domain.CampaignRunning, 5, 1, 5)
	b := f.addCampaign(domain.CampaignRunning, 5, 1, 5)

	f.poll()
	require.Len(t, f.tel.dials(), 3)
	// Answered calls keep their slot until OnCallEnded.
	f.poll()
	require.Len(t, f.tel.dials(), 3)

	first := f.tel.dials()[0]
	f.endCall(first.CallID, domain.StatusCompleted, "hangup_customer")
	f.poll()
	assert.Len(t, f.tel.dials(), 4)

	activeA, _ := f.campaigns.CountActiveTargets(context.Background(), a.ID)
	activeB, _ := f.campaigns.CountActiveTargets(context.Background(), b.ID)
	assert.Equal(t, 3, activeA+activeB)
}

func TestNoAnswerRetriesWithBackoffThenFails(t *testing.T) {
	f := newFixture(t, Options{RetryBackoff: 5 * time.Minute})
	f.tel.setScript(func(domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
		return domain.OutboundCallResult{Error: "sip status 486: Busy Here"}, nil
	})
	c := f.addCampaign(domain.CampaignRunning, 1, 2, 1)
	targetID := f.campaigns.allTargets(c.ID)[0].ID
	start := f.clock.Now()

	f.poll()
	require.Len(t, f.tel.dials(), 1)
	tg := f.campaigns.target(c.ID, targetID)
	assert.Equal(t, domain.TargetPending, tg.Status)
	assert.Equal(t, 1, tg.Attempts)
	require.NotNil(t, tg.NextTryAt)
	assert.Equal(t, start.Add(5*time.Minute), *tg.NextTryAt)
	assert.Equal(t, "sip status 486: Busy Here", tg.LastError)

	call := f.call(*tg.CallID)
	assert.Equal(t, domain.StatusBusy, call.Status)
	assert.Equal(t, "busy", call.EndReason)
	assert.Equal(t, 0, call.DurationSec)
	require.NotNil(t, call.EndedAt)

	// Not due yet.
	f.clock.Advance(4 * time.Minute)
	f.poll()
	assert.Len(t, f.tel.dials(), 1)

	// Due: second (last) attempt.
	f.clock.Advance(time.Minute)
	f.poll()
	require.Len(t, f.tel.dials(), 2)
	tg = f.campaigns.target(c.ID, targetID)
	assert.Equal(t, domain.TargetFailed, tg.Status)
	assert.Equal(t, 2, tg.Attempts)
	assert.Nil(t, tg.NextTryAt)
	assert.Contains(t, tg.LastError, "Busy")

	got := f.campaign(c.ID)
	assert.Equal(t, 1, got.Failed)
	assert.Equal(t, 0, got.Completed)
	assert.Equal(t, domain.CampaignCompleted, got.Status)

	// A completed campaign is no longer polled.
	f.clock.Advance(time.Hour)
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
}

func TestAnsweredCallCompletesCampaignOnCallEnded(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 2, 2, 2)

	f.poll()
	dials := f.tel.dials()
	require.Len(t, dials, 2)

	// Dial request and call row carry the campaign metadata.
	req := dials[0]
	assert.True(t, req.WaitUntilAnswered)
	assert.Equal(t, 30*time.Second, req.RingTimeout)
	assert.Equal(t, f.sip.Number, req.FromNumber.Number)
	assert.Equal(t, f.profile.ID, req.AgentProfile.ID)
	assert.Equal(t, "outbound", req.Metadata["direction"])
	assert.Equal(t, c.ID.String(), req.Metadata["campaignId"])
	assert.Equal(t, req.CallID.String(), req.Metadata["callId"])
	assert.NotEmpty(t, req.Metadata["targetId"])
	assert.NotNil(t, req.Metadata["vars"])

	call := f.call(req.CallID)
	assert.Equal(t, domain.StatusActive, call.Status)
	assert.Equal(t, domain.DirectionOutbound, call.Direction)
	assert.Equal(t, "call-"+call.ID.String(), call.RoomName)
	assert.Equal(t, req.RoomName, call.RoomName)
	assert.Equal(t, f.sip.Number, call.FromNumber)
	assert.Equal(t, req.ToNumber, call.ToNumber)
	assert.Equal(t, c.ID, *call.CampaignID)
	assert.Equal(t, f.sip.ID, *call.SIPNumberID)
	assert.Equal(t, f.profile.ID, *call.AgentProfileID)
	require.NotNil(t, call.ContactID)
	require.NotNil(t, call.AnsweredAt)
	assert.Equal(t, "PA_1", call.ParticipantID)
	assert.Equal(t, req.Metadata["targetId"], call.Metadata["targetId"])
	assert.Equal(t, 2, f.contacts.count())

	targetID, err := uuid.Parse(call.Metadata["targetId"].(string))
	require.NoError(t, err)
	tg := f.campaigns.target(c.ID, targetID)
	assert.Equal(t, domain.TargetCalling, tg.Status)
	assert.Equal(t, 1, tg.Attempts)
	assert.Equal(t, call.ID, *tg.CallID)
	assert.Equal(t, *call.ContactID, *tg.ContactID)

	f.endCall(dials[0].CallID, domain.StatusCompleted, "hangup_customer")
	assert.Equal(t, domain.TargetDone, f.campaigns.target(c.ID, targetID).Status)
	assert.Equal(t, domain.CampaignRunning, f.campaign(c.ID).Status)

	f.endCall(dials[1].CallID, domain.StatusVoicemail, "voicemail")
	got := f.campaign(c.ID)
	assert.Equal(t, 2, got.Completed)
	assert.Equal(t, 0, got.Failed)
	assert.Equal(t, domain.CampaignCompleted, got.Status)
}

func TestAnsweredCallFailureIsRetried(t *testing.T) {
	f := newFixture(t, Options{RetryBackoff: time.Minute})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 1, 2, 1)

	f.poll()
	f.endCall(f.tel.dials()[0].CallID, domain.StatusFailed, "failed")
	tg := f.campaigns.allTargets(c.ID)[0]
	assert.Equal(t, domain.TargetPending, tg.Status)
	assert.Equal(t, "failed", tg.LastError)

	f.clock.Advance(time.Minute)
	f.poll()
	require.Len(t, f.tel.dials(), 2)
	assert.Equal(t, 2, f.campaigns.allTargets(c.ID)[0].Attempts)
}

func TestOnCallEndedIsIdempotent(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 1, 1, 2)

	f.poll()
	callID := f.tel.dials()[0].CallID
	ended := f.endCall(callID, domain.StatusCompleted, "hangup_agent")
	progress := f.bus.count(domain.EventCampaignProgress)

	f.engine.OnCallEnded(context.Background(), ended)
	f.engine.OnCallEnded(context.Background(), ended)
	// A fresh engine (after restart) must not double count either.
	f.newEngine().OnCallEnded(context.Background(), ended)

	got := f.campaign(c.ID)
	assert.Equal(t, 1, got.Completed)
	assert.Equal(t, progress, f.bus.count(domain.EventCampaignProgress))

	// Non-campaign and non-terminal calls are ignored.
	f.engine.OnCallEnded(context.Background(), nil)
	f.engine.OnCallEnded(context.Background(), &domain.Call{ID: uuid.New(), Status: domain.StatusCompleted})
	active := f.call(callID)
	active.Status = domain.StatusActive
	f.engine.OnCallEnded(context.Background(), &active)
	assert.Equal(t, 1, f.campaign(c.ID).Completed)
}

func TestOnCallEndedAfterRestartFindsTargetByScan(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 1, 1, 1)
	f.poll()
	callID := f.tel.dials()[0].CallID

	// Simulate a restart: a new engine without in-memory slots; the call
	// metadata lost its targetId, so the target is found by CallID.
	f.engine = f.newEngine()
	call := f.call(callID)
	call.Status = domain.StatusCompleted
	call.Metadata = nil
	require.NoError(t, f.calls.UpdateCall(context.Background(), &call))
	f.engine.OnCallEnded(context.Background(), &call)

	assert.Equal(t, domain.TargetDone, f.campaigns.allTargets(c.ID)[0].Status)
	got := f.campaign(c.ID)
	assert.Equal(t, 1, got.Completed)
	assert.Equal(t, domain.CampaignCompleted, got.Status)
}

func TestStartAndPause(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.tel.gate = make(chan struct{})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignDraft, 1, 3, 3)

	// Draft campaigns are not dialed.
	f.poll()
	assert.Empty(t, f.tel.dials())

	require.NoError(t, f.engine.Start(ctx, c.ID))
	require.NoError(t, f.engine.Start(ctx, c.ID)) // idempotent
	assert.Equal(t, domain.CampaignRunning, f.campaign(c.ID).Status)

	f.engine.pollOnce(ctx)
	require.Eventually(t, func() bool { return len(f.tel.dials()) == 1 }, time.Second, time.Millisecond)

	require.NoError(t, f.engine.Pause(ctx, c.ID))
	require.NoError(t, f.engine.Pause(ctx, c.ID)) // idempotent
	assert.Equal(t, domain.CampaignPaused, f.campaign(c.ID).Status)

	// The in-flight call finishes and is settled while paused.
	f.tel.gate <- struct{}{}
	f.engine.wg.Wait()
	first := f.campaigns.target(c.ID, *targetOfCall(f, f.tel.dials()[0].CallID))
	assert.Equal(t, domain.TargetPending, first.Status)

	// Paused: no claiming, even when targets are due.
	claims := f.campaigns.claimCount()
	f.clock.Advance(time.Hour)
	f.engine.pollOnce(ctx)
	f.engine.pollOnce(ctx)
	assert.Len(t, f.tel.dials(), 1)
	assert.Equal(t, claims, f.campaigns.claimCount())

	require.NoError(t, f.engine.Start(ctx, c.ID))
	close(f.tel.gate)
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
}

func targetOfCall(f *fixture, callID uuid.UUID) *uuid.UUID {
	call := f.call(callID)
	id := targetIDFromMetadata(call.Metadata)
	return &id
}

func TestStartValidation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})

	_, err := f.campaigns.GetCampaign(ctx, uuid.New())
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.ErrorIs(t, f.engine.Start(ctx, uuid.New()), domain.ErrNotFound)

	empty := f.addCampaign(domain.CampaignDraft, 1, 1, 0)
	require.ErrorIs(t, f.engine.Start(ctx, empty.ID), domain.ErrInvalid)

	done := f.addCampaign(domain.CampaignCompleted, 1, 1, 1)
	require.ErrorIs(t, f.engine.Start(ctx, done.ID), domain.ErrConflict)
	require.ErrorIs(t, f.engine.Pause(ctx, done.ID), domain.ErrConflict)

	noSIP := f.addCampaign(domain.CampaignDraft, 1, 1, 1)
	noSIP.SIPNumberID = nil
	require.NoError(t, f.campaigns.UpdateCampaign(ctx, &noSIP))
	require.ErrorIs(t, f.engine.Start(ctx, noSIP.ID), domain.ErrInvalid)

	inbound := domain.SIPNumber{ID: uuid.New(), OrgID: f.orgID, Number: "+97677000000", AllowInbound: true}
	require.NoError(t, f.numbers.CreateSIPNumber(ctx, &inbound))
	inboundOnly := f.addCampaign(domain.CampaignDraft, 1, 1, 1)
	inboundOnly.SIPNumberID = &inbound.ID
	require.NoError(t, f.campaigns.UpdateCampaign(ctx, &inboundOnly))
	require.ErrorIs(t, f.engine.Start(ctx, inboundOnly.ID), domain.ErrInvalid)

	missingSIP := f.addCampaign(domain.CampaignDraft, 1, 1, 1)
	gone := uuid.New()
	missingSIP.SIPNumberID = &gone
	require.NoError(t, f.campaigns.UpdateCampaign(ctx, &missingSIP))
	require.ErrorIs(t, f.engine.Start(ctx, missingSIP.ID), domain.ErrInvalid)

	ok := f.addCampaign(domain.CampaignDraft, 1, 1, 1)
	require.NoError(t, f.engine.Start(ctx, ok.ID))
	assert.Equal(t, domain.CampaignRunning, f.campaign(ok.ID).Status)
	require.ErrorIs(t, f.engine.Pause(ctx, empty.ID), domain.ErrConflict)
}

func TestEventsPublishedInOrder(t *testing.T) {
	f := newFixture(t, Options{})
	c := f.addCampaign(domain.CampaignRunning, 1, 1, 2)

	f.tel.setScript(noAnswer)
	f.poll()
	missed := f.tel.dials()[0].CallID
	assert.Equal(t, []domain.EventType{
		domain.EventCallStarted,
		domain.EventCallRinging,
		domain.EventCallEnded,
		domain.EventCampaignProgress,
	}, f.bus.typesFor(missed))
	assert.Equal(t, domain.StatusNoAnswer, f.call(missed).Status)

	f.tel.setScript(answered)
	f.poll()
	picked := f.tel.dials()[1].CallID
	assert.Equal(t, []domain.EventType{
		domain.EventCallStarted,
		domain.EventCallRinging,
		domain.EventCallAnswered,
	}, f.bus.typesFor(picked))

	f.endCall(picked, domain.StatusCompleted, "hangup_customer")
	assert.Equal(t, []domain.EventType{
		domain.EventCallStarted,
		domain.EventCallRinging,
		domain.EventCallAnswered,
		domain.EventCampaignProgress,
	}, f.bus.typesFor(picked))

	// Payload shapes follow docs/EVENTS.md.
	evs := f.bus.all()
	last := evs[len(evs)-1]
	require.Equal(t, domain.EventCampaignProgress, last.Type)
	payload := last.Payload.(map[string]any)
	camp := payload["campaign"].(domain.Campaign)
	assert.Equal(t, c.ID, camp.ID)
	assert.Equal(t, domain.CampaignCompleted, camp.Status)
	assert.Equal(t, domain.TargetDone, payload["target"].(*domain.CampaignTarget).Status)
	for _, ev := range evs {
		assert.NotEmpty(t, ev.ID)
		assert.Equal(t, f.orgID, ev.OrgID)
	}
	for _, ev := range evs {
		if ev.Type == domain.EventCallEnded {
			p := ev.Payload.(map[string]any)
			assert.Equal(t, "no_answer", p["endReason"])
			assert.Equal(t, 0, p["durationSec"])
			assert.IsType(t, domain.Call{}, p["call"])
		}
	}
}

func TestReconcileStuckTargets(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{MaxCallDuration: 10 * time.Minute, RetryBackoff: time.Minute})
	c := f.addCampaign(domain.CampaignRunning, 5, 3, 5)
	targets := f.campaigns.allTargets(c.ID)
	old := f.clock.Now()

	mkCall := func(status domain.CallStatus, started time.Time) uuid.UUID {
		campaignID := c.ID
		call := &domain.Call{ID: uuid.New(), OrgID: f.orgID, CampaignID: &campaignID, Status: status,
			Direction: domain.DirectionOutbound, StartedAt: started, EndReason: "hangup_customer"}
		require.NoError(t, f.calls.CreateCall(ctx, call))
		return call.ID
	}
	stick := func(i int, callID *uuid.UUID, attempts int) {
		tg := targets[i]
		tg.Status = domain.TargetCalling
		tg.CallID = callID
		tg.Attempts = attempts
		tg.UpdatedAt = old
		require.NoError(t, f.campaigns.UpdateTarget(ctx, &tg))
	}
	completed := mkCall(domain.StatusCompleted, old)
	stick(0, &completed, 1)
	stick(1, nil, 0) // claimed, crashed before the call row existed
	missing := uuid.New()
	stick(2, &missing, 1) // call row missing
	longAgo := mkCall(domain.StatusActive, old)
	stick(3, &longAgo, 1)

	f.clock.Advance(21 * time.Minute) // > 2×MaxCallDuration
	live := mkCall(domain.StatusActive, f.clock.Now().Add(-time.Minute))
	stick(4, &live, 1)
	tg4 := f.campaigns.target(c.ID, targets[4].ID)
	tg4.UpdatedAt = f.clock.Now()
	require.NoError(t, f.campaigns.UpdateTarget(ctx, &tg4))

	require.NoError(t, f.engine.reconcile(ctx))

	got := func(i int) domain.CampaignTarget { return f.campaigns.target(c.ID, targets[i].ID) }
	assert.Equal(t, domain.TargetDone, got(0).Status)
	assert.Equal(t, domain.TargetPending, got(1).Status)
	assert.Equal(t, 0, got(1).Attempts)
	assert.Equal(t, domain.TargetPending, got(2).Status)
	assert.Equal(t, 0, got(2).Attempts, "attempt refunded when the call row is missing")
	assert.Equal(t, domain.TargetPending, got(3).Status)
	assert.Equal(t, 1, got(3).Attempts)
	assert.Equal(t, domain.StatusFailed, f.call(longAgo).Status)
	assert.Equal(t, "max_duration", f.call(longAgo).EndReason)
	assert.Equal(t, domain.TargetCalling, got(4).Status, "a live call is left alone")
	assert.Equal(t, domain.StatusActive, f.call(live).Status)
	assert.Equal(t, 1, f.campaign(c.ID).Completed)

	// The live call ends later and is settled via the scan path.
	f.endCall(live, domain.StatusCompleted, "hangup_customer")
	assert.Equal(t, domain.TargetDone, got(4).Status)
	assert.Equal(t, 2, f.campaign(c.ID).Completed)
}

func TestStaleAnsweredCallIsSwept(t *testing.T) {
	f := newFixture(t, Options{MaxCallDuration: 5 * time.Minute, RetryBackoff: time.Minute})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 1, 2, 1)
	f.poll()
	callID := f.tel.dials()[0].CallID

	f.clock.Advance(9 * time.Minute)
	f.poll()
	assert.Len(t, f.tel.dials(), 1, "slot held while the call may still be live")

	f.clock.Advance(2 * time.Minute) // > 2×MaxCallDuration since answer
	f.poll()
	assert.Equal(t, domain.StatusFailed, f.call(callID).Status)
	tg := f.campaigns.allTargets(c.ID)[0]
	assert.Equal(t, domain.TargetPending, tg.Status)

	f.clock.Advance(time.Minute)
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
}

func TestShutdownRequeuesInFlightTarget(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.gate = make(chan struct{}) // never released: Dial waits for ctx
	c := f.addCampaign(domain.CampaignRunning, 1, 1, 1)

	ctx, cancel := context.WithCancel(context.Background())
	f.engine.pollOnce(ctx)
	require.Eventually(t, func() bool { return len(f.tel.dials()) == 1 }, time.Second, time.Millisecond)
	cancel()
	f.engine.wg.Wait()

	tg := f.campaigns.allTargets(c.ID)[0]
	assert.Equal(t, domain.TargetPending, tg.Status)
	assert.Equal(t, 0, tg.Attempts, "shutdown does not consume an attempt")
	assert.Equal(t, domain.CampaignRunning, f.campaign(c.ID).Status)
	assert.Equal(t, domain.StatusFailed, f.call(f.tel.dials()[0].CallID).Status)
}

func TestRunDialsUntilCampaignCompletes(t *testing.T) {
	clock := newFakeClock()
	f := newFixture(t, Options{PollInterval: 5 * time.Millisecond, Clock: clock.Now})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignRunning, 3, 1, 6)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.engine.Run(ctx) }()

	require.Eventually(t, func() bool {
		return f.campaign(c.ID).Status == domain.CampaignCompleted
	}, 3*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	got := f.campaign(c.ID)
	assert.Equal(t, 6, got.Failed)
	assert.Len(t, f.tel.dials(), 6)
	assert.LessOrEqual(t, f.tel.maxInflight.Load(), int32(3))
}

func TestIdleSyncCompletesCampaignAndFixesCounters(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	c := f.addCampaign(domain.CampaignRunning, 1, 1, 2)
	for _, tg := range f.campaigns.allTargets(c.ID) {
		tg.Status = domain.TargetDone
		require.NoError(t, f.campaigns.UpdateTarget(ctx, &tg))
	}
	f.poll()
	got := f.campaign(c.ID)
	assert.Equal(t, domain.CampaignCompleted, got.Status)
	assert.Equal(t, 2, got.Completed)
	assert.Empty(t, f.tel.dials())
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		res  domain.OutboundCallResult
		err  error
		want domain.CallStatus
	}{
		{"not answered, no error", domain.OutboundCallResult{}, nil, domain.StatusNoAnswer},
		{"busy result", domain.OutboundCallResult{Error: "486 Busy Here"}, nil, domain.StatusBusy},
		{"decline", domain.OutboundCallResult{}, errors.New("sip status: 603 Decline"), domain.StatusBusy},
		{"sip timeout", domain.OutboundCallResult{}, errors.New("sip request timed out"), domain.StatusNoAnswer},
		{"unavailable", domain.OutboundCallResult{Error: "480 Temporarily Unavailable"}, nil, domain.StatusNoAnswer},
		{"deadline", domain.OutboundCallResult{}, fmt.Errorf("dial: %w", context.DeadlineExceeded), domain.StatusNoAnswer},
		{"trunk failure", domain.OutboundCallResult{}, errors.New("503 Service Unavailable trunk down"), domain.StatusFailed},
		{"generic error", domain.OutboundCallResult{}, errors.New("twirp: internal error"), domain.StatusFailed},
		{"forbidden", domain.OutboundCallResult{Error: "403 Forbidden"}, nil, domain.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classify(tc.res, tc.err))
		})
	}
}
