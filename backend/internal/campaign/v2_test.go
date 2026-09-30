package campaign

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var v2Outcomes = []domain.CampaignOutcome{
	{Code: "agreed", Label: "Зөвшөөрсөн", Terminal: true},
	{Code: "declined", Label: "Татгалзсан", Terminal: true},
	{Code: "callback", Label: "Дахин залгах", Terminal: false},
	{Code: "no_contact", Label: "Холбогдоогүй", Terminal: true},
}

// configure mutates a stored campaign.
func (f *fixture) configure(id uuid.UUID, fn func(c *domain.Campaign)) domain.Campaign {
	f.t.Helper()
	c := f.campaign(id)
	fn(&c)
	require.NoError(f.t, f.campaigns.UpdateCampaign(context.Background(), &c))
	return c
}

// endCallWithOutcome is endCall with the AI-chosen outcome on the call.
func (f *fixture) endCallWithOutcome(callID uuid.UUID, status domain.CallStatus, reason, code, note string) {
	f.t.Helper()
	c := f.call(callID)
	now := f.clock.Now()
	c.Status = status
	c.EndReason = reason
	c.EndedAt = &now
	setCallOutcome(&c, code, note)
	require.NoError(f.t, f.calls.UpdateCall(context.Background(), &c))
	f.engine.OnCallEnded(context.Background(), &c)
}

// progressCampaigns returns the campaign snapshots of campaign.progress events.
func (f *fixture) progressCampaigns(id uuid.UUID) []domain.Campaign {
	var out []domain.Campaign
	for _, ev := range f.bus.all() {
		if ev.Type != domain.EventCampaignProgress {
			continue
		}
		c := ev.Payload.(map[string]any)["campaign"].(domain.Campaign)
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

func TestDryRunPausesAfterLimitThenFullRunContinues(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignDraft, 5, 1, 5)

	require.ErrorIs(t, f.engine.Start(ctx, c.ID, -1), domain.ErrInvalid)
	require.NoError(t, f.engine.Start(ctx, c.ID, 2))
	got := f.campaign(c.ID)
	assert.Equal(t, domain.CampaignRunning, got.Status)
	assert.Equal(t, 2, got.DryRunLimit)
	assert.Zero(t, got.DryRunDialed)

	f.poll()
	require.Len(t, f.tel.dials(), 2, "only the dry-run limit is dialled despite concurrency 5")
	got = f.campaign(c.ID)
	assert.Equal(t, domain.CampaignPaused, got.Status)
	assert.Equal(t, 2, got.DryRunDialed)

	snaps := f.progressCampaigns(c.ID)
	require.NotEmpty(t, snaps)
	last := snaps[len(snaps)-1]
	assert.Equal(t, domain.CampaignPaused, last.Status)
	assert.Equal(t, last.DryRunLimit, last.DryRunDialed)

	// Paused: nothing more is dialled; the dry-run calls settle normally.
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
	for _, d := range f.tel.dials() {
		f.endCall(d.CallID, domain.StatusCompleted, "hangup_customer")
	}
	got = f.campaign(c.ID)
	assert.Equal(t, 2, got.Completed)
	assert.Equal(t, domain.CampaignPaused, got.Status)

	// Full run continues with the remaining targets.
	require.NoError(t, f.engine.Start(ctx, c.ID, 0))
	got = f.campaign(c.ID)
	assert.Equal(t, domain.CampaignRunning, got.Status)
	assert.Zero(t, got.DryRunLimit)
	assert.Zero(t, got.DryRunDialed)
	f.poll()
	assert.Len(t, f.tel.dials(), 5)
	assert.Equal(t, domain.CampaignRunning, f.campaign(c.ID).Status)
}

func TestDryRunCountsAcrossPolls(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignDraft, 1, 1, 4)
	require.NoError(t, f.engine.Start(ctx, c.ID, 2))

	f.poll()
	require.Len(t, f.tel.dials(), 1)
	got := f.campaign(c.ID)
	assert.Equal(t, domain.CampaignRunning, got.Status)
	assert.Equal(t, 1, got.DryRunDialed)

	f.poll()
	require.Len(t, f.tel.dials(), 2)
	assert.Equal(t, domain.CampaignPaused, f.campaign(c.ID).Status)

	// A new dry run on the paused campaign dials the next batch.
	require.NoError(t, f.engine.Start(ctx, c.ID, 1))
	f.poll()
	require.Len(t, f.tel.dials(), 3)
	got = f.campaign(c.ID)
	assert.Equal(t, domain.CampaignPaused, got.Status)
	assert.Equal(t, 1, got.DryRunDialed)
	assert.Equal(t, 1, got.DryRunLimit)
}

func TestDryRunConvertedToFullRunWhileRunning(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignDraft, 1, 1, 3)
	require.NoError(t, f.engine.Start(ctx, c.ID, 2))
	f.poll()
	require.NoError(t, f.engine.Start(ctx, c.ID, 5), "running: dry-run start is a no-op")
	assert.Equal(t, 2, f.campaign(c.ID).DryRunLimit)
	require.NoError(t, f.engine.Start(ctx, c.ID, 0))
	got := f.campaign(c.ID)
	assert.Equal(t, domain.CampaignRunning, got.Status)
	assert.Zero(t, got.DryRunLimit)
	f.poll()
	f.poll()
	assert.Len(t, f.tel.dials(), 3)
	assert.Equal(t, domain.CampaignCompleted, f.campaign(c.ID).Status)
}

func TestDryRunLimitAlreadyReachedPausesOnPoll(t *testing.T) {
	f := newFixture(t, Options{})
	c := f.addCampaign(domain.CampaignRunning, 2, 1, 3)
	f.configure(c.ID, func(c *domain.Campaign) { c.DryRunLimit, c.DryRunDialed = 2, 2 })
	f.poll()
	assert.Empty(t, f.tel.dials())
	assert.Equal(t, domain.CampaignPaused, f.campaign(c.ID).Status)
}

func TestScheduleWindowGatesDialing(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignRunning, 2, 1, 2)
	// The fake clock starts at 10:00 UTC on Wednesday = 18:00 in Ulaanbaatar.
	f.configure(c.ID, func(c *domain.Campaign) {
		c.Schedule = domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"}
	})

	claims := f.campaigns.claimCount()
	f.poll()
	assert.Empty(t, f.tel.dials())
	assert.Equal(t, claims, f.campaigns.claimCount(), "outside the window nothing is claimed")
	assert.Equal(t, domain.CampaignRunning, f.campaign(c.ID).Status, "the campaign stays running")

	f.clock.Advance(14*time.Hour + 59*time.Minute) // 08:59 Thursday UB
	f.poll()
	assert.Empty(t, f.tel.dials())

	f.clock.Advance(time.Minute) // 09:00 UB
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
}

func TestScheduleWeekdaysAndInvalidTimezone(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(noAnswer)
	weekend := f.addCampaign(domain.CampaignRunning, 1, 1, 1)
	f.configure(weekend.ID, func(c *domain.Campaign) {
		c.Schedule = domain.CampaignSchedule{Weekdays: []time.Weekday{time.Saturday, time.Sunday}}
	})
	// Invalid time zone → UTC; 10:00 UTC is inside 09:00–17:00.
	badTZ := f.addCampaign(domain.CampaignRunning, 1, 1, 1)
	f.configure(badTZ.ID, func(c *domain.Campaign) {
		c.Schedule = domain.CampaignSchedule{Timezone: "Mars/Olympus", StartTime: "09:00", EndTime: "17:00"}
	})

	f.poll()
	dials := f.tel.dials()
	require.Len(t, dials, 1)
	assert.Equal(t, badTZ.ID.String(), dials[0].Metadata["campaignId"])

	f.clock.Advance(3 * 24 * time.Hour) // Saturday
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) count(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), s)
}

func TestOutsideWindowLogIsRateLimited(t *testing.T) {
	f := newFixture(t, Options{})
	var logs syncBuffer
	f.engine = NewEngine(f.campaigns, f.calls, f.contacts, f.numbers, f.profiles, f.dnc, f.tel, f.bus, f.opts,
		zerolog.New(&logs).Level(zerolog.DebugLevel))
	c := f.addCampaign(domain.CampaignRunning, 1, 1, 1)
	f.configure(c.ID, func(c *domain.Campaign) {
		c.Schedule = domain.CampaignSchedule{Timezone: "Bad/Zone", StartTime: "00:00", EndTime: "01:00"}
	})
	const msg = "outside its calling window"
	f.poll()
	f.poll()
	f.clock.Advance(30 * time.Second)
	f.poll()
	assert.Equal(t, 1, logs.count(msg))
	f.clock.Advance(30 * time.Second)
	f.poll()
	assert.Equal(t, 2, logs.count(msg))
	assert.Equal(t, 1, logs.count("invalid campaign schedule time zone"), "bad time zone logged once")
	assert.Empty(t, f.tel.dials())
}

func TestPacingLimitsNewDials(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(noAnswer)
	c := f.addCampaign(domain.CampaignRunning, 10, 1, 10)
	f.configure(c.ID, func(c *domain.Campaign) { c.Schedule = domain.CampaignSchedule{PacePerMinute: 2} })

	f.poll()
	assert.Len(t, f.tel.dials(), 2, "burst = pace")
	f.poll()
	assert.Len(t, f.tel.dials(), 2, "bucket empty")
	f.clock.Advance(29 * time.Second)
	f.poll()
	assert.Len(t, f.tel.dials(), 2)
	f.clock.Advance(time.Second)
	f.poll()
	assert.Len(t, f.tel.dials(), 3, "2/min refills one token per 30s")
	f.clock.Advance(10 * time.Minute)
	f.poll()
	assert.Len(t, f.tel.dials(), 5, "refill capped at the burst")

	// Removing the pace lifts the limit.
	f.configure(c.ID, func(c *domain.Campaign) { c.Schedule = domain.CampaignSchedule{} })
	f.poll()
	assert.Len(t, f.tel.dials(), 10)
}

func TestDoNotCallTargetsAreSkipped(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 5, 1, 3)
	targets := f.campaigns.allTargets(c.ID)
	require.NoError(t, f.dnc.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: f.orgID, Phone: targets[1].Phone}))
	// Same number in another organisation does not matter.
	require.NoError(t, f.dnc.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: uuid.New(), Phone: targets[0].Phone}))

	f.poll()
	dials := f.tel.dials()
	require.Len(t, dials, 2)
	for _, d := range dials {
		assert.NotEqual(t, targets[1].Phone, d.ToNumber)
	}
	skipped := f.campaigns.target(c.ID, targets[1].ID)
	assert.Equal(t, domain.TargetSkipped, skipped.Status)
	assert.Equal(t, "do_not_call", skipped.LastError)
	assert.Zero(t, skipped.Attempts)
	assert.Nil(t, skipped.CallID)
	assert.Equal(t, 1, f.campaign(c.ID).Skipped)
	assert.Len(t, f.calls.all(), 2, "no call row for the skipped target")

	var sawSkipped bool
	for _, ev := range f.bus.all() {
		if ev.Type != domain.EventCampaignProgress {
			continue
		}
		if tg, _ := ev.Payload.(map[string]any)["target"].(*domain.CampaignTarget); tg != nil && tg.ID == targets[1].ID {
			sawSkipped = tg.Status == domain.TargetSkipped
		}
	}
	assert.True(t, sawSkipped, "campaign.progress published for the skipped target")

	for _, d := range dials {
		f.endCall(d.CallID, domain.StatusCompleted, "hangup_customer")
	}
	got := f.campaign(c.ID)
	assert.Equal(t, 2, got.Completed)
	assert.Equal(t, 1, got.Skipped)
	assert.Equal(t, domain.CampaignCompleted, got.Status, "completed + skipped = total")
}

func TestAllTargetsOnDoNotCallCompletesCampaign(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	c := f.addCampaign(domain.CampaignRunning, 5, 1, 2)
	for _, tg := range f.campaigns.allTargets(c.ID) {
		require.NoError(t, f.dnc.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: f.orgID, Phone: tg.Phone}))
	}
	f.poll()
	assert.Empty(t, f.tel.dials())
	got := f.campaign(c.ID)
	assert.Equal(t, 2, got.Skipped)
	assert.Equal(t, domain.CampaignCompleted, got.Status)

	// The idle sync keeps the skipped counter right.
	f.configure(c.ID, func(c *domain.Campaign) { c.Skipped, c.Status = 0, domain.CampaignRunning })
	require.NoError(t, f.engine.syncCampaign(ctx, c.ID))
	got = f.campaign(c.ID)
	assert.Equal(t, 2, got.Skipped)
	assert.Equal(t, domain.CampaignCompleted, got.Status)
}

func TestDoNotCallCheckFailureDialsNothing(t *testing.T) {
	f := newFixture(t, Options{})
	c := f.addCampaign(domain.CampaignRunning, 3, 1, 3)
	f.dnc.setErr(errors.New("db down"))
	f.poll()
	assert.Empty(t, f.tel.dials())
	for _, tg := range f.campaigns.allTargets(c.ID) {
		assert.Equal(t, domain.TargetPending, tg.Status, "claimed targets are given back")
		assert.Zero(t, tg.Attempts)
	}
	f.dnc.setErr(nil)
	f.poll()
	assert.Len(t, f.tel.dials(), 3)
}

func TestNonTerminalOutcomeRequeuesTarget(t *testing.T) {
	f := newFixture(t, Options{RetryBackoff: 10 * time.Minute})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 1, 2, 1)
	f.configure(c.ID, func(c *domain.Campaign) { c.Outcomes = v2Outcomes })
	targetID := f.campaigns.allTargets(c.ID)[0].ID

	f.poll()
	start := f.clock.Now()
	f.endCallWithOutcome(f.tel.dials()[0].CallID, domain.StatusCompleted, "hangup_customer", "callback", "Маргааш 10 цагт")
	tg := f.campaigns.target(c.ID, targetID)
	assert.Equal(t, domain.TargetPending, tg.Status)
	assert.Equal(t, "callback", tg.Outcome)
	assert.Equal(t, "Маргааш 10 цагт", tg.OutcomeNote)
	require.NotNil(t, tg.NextTryAt)
	assert.Equal(t, start.Add(10*time.Minute), *tg.NextTryAt)
	assert.Empty(t, tg.LastError)
	got := f.campaign(c.ID)
	assert.Zero(t, got.Completed)
	assert.Equal(t, domain.CampaignRunning, got.Status)

	f.poll()
	assert.Len(t, f.tel.dials(), 1, "not due before the backoff")
	f.clock.Advance(10 * time.Minute)
	f.poll()
	require.Len(t, f.tel.dials(), 2)

	// Attempts exhausted: a non-terminal outcome finishes the target.
	f.endCallWithOutcome(f.tel.dials()[1].CallID, domain.StatusCompleted, "hangup_agent", "callback", "Again later")
	tg = f.campaigns.target(c.ID, targetID)
	assert.Equal(t, domain.TargetDone, tg.Status)
	assert.Equal(t, 2, tg.Attempts)
	assert.Equal(t, "callback", tg.Outcome)
	assert.Equal(t, "Again later", tg.OutcomeNote)
	got = f.campaign(c.ID)
	assert.Equal(t, 1, got.Completed)
	assert.Equal(t, domain.CampaignCompleted, got.Status)
}

func TestTerminalUnknownAndUndefinedOutcomesFinishTarget(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	withOutcomes := f.addCampaign(domain.CampaignRunning, 3, 3, 3)
	f.configure(withOutcomes.ID, func(c *domain.Campaign) { c.Outcomes = v2Outcomes })
	f.poll()
	dials := f.tel.dials()
	require.Len(t, dials, 3)
	codes := []string{"agreed", "bogus", ""}
	for i, d := range dials {
		f.endCallWithOutcome(d.CallID, domain.StatusCompleted, "hangup_customer", codes[i], "note")
	}
	for i, d := range dials {
		tg := f.campaigns.target(withOutcomes.ID, *targetOfCall(f, d.CallID))
		assert.Equalf(t, domain.TargetDone, tg.Status, "outcome %q is terminal", codes[i])
		assert.Equal(t, codes[i], tg.Outcome)
	}
	assert.Equal(t, domain.CampaignCompleted, f.campaign(withOutcomes.ID).Status)

	// A campaign without outcomes ignores "callback".
	plain := f.addCampaign(domain.CampaignRunning, 1, 3, 1)
	f.poll()
	d := f.tel.dials()[3]
	f.endCallWithOutcome(d.CallID, domain.StatusCompleted, "hangup_customer", "callback", "")
	tg := f.campaigns.target(plain.ID, *targetOfCall(f, d.CallID))
	assert.Equal(t, domain.TargetDone, tg.Status)
	assert.Equal(t, "callback", tg.Outcome)
}

func TestFailedAnsweredCallCopiesOutcomeAndRetries(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(answered)
	c := f.addCampaign(domain.CampaignRunning, 1, 2, 1)
	f.configure(c.ID, func(c *domain.Campaign) { c.Outcomes = v2Outcomes })
	f.poll()
	f.endCallWithOutcome(f.tel.dials()[0].CallID, domain.StatusFailed, "failed", "declined", "Dropped")
	tg := f.campaigns.allTargets(c.ID)[0]
	assert.Equal(t, domain.TargetPending, tg.Status, "failed calls follow the retry rules")
	assert.Equal(t, "declined", tg.Outcome)
	assert.Equal(t, "failed", tg.LastError)
}

func TestUnansweredCallGetsNoContactOutcome(t *testing.T) {
	f := newFixture(t, Options{})
	f.tel.setScript(noAnswer)
	withCode := f.addCampaign(domain.CampaignRunning, 1, 1, 1)
	f.configure(withCode.ID, func(c *domain.Campaign) { c.Outcomes = v2Outcomes })
	f.poll()
	require.Len(t, f.tel.dials(), 1)
	tg := f.campaigns.allTargets(withCode.ID)[0]
	assert.Equal(t, domain.TargetFailed, tg.Status)
	assert.Equal(t, "no_contact", tg.Outcome)
	assert.Empty(t, tg.OutcomeNote)
	code, _ := callOutcome(new(f.call(f.tel.dials()[0].CallID)))
	assert.Equal(t, "no_contact", code, "the call carries the outcome too")
	var endedOutcome any
	for _, ev := range f.bus.all() {
		if ev.Type == domain.EventCallEnded && *ev.CallID == f.tel.dials()[0].CallID {
			endedOutcome = ev.Payload.(map[string]any)["outcome"]
		}
	}
	assert.Equal(t, "no_contact", endedOutcome, "call.ended carries the outcome")

	// Without a no_contact code the outcome stays empty (and clears an old one).
	noCode := f.addCampaign(domain.CampaignRunning, 1, 2, 1)
	f.configure(noCode.ID, func(c *domain.Campaign) { c.Outcomes = v2Outcomes[:3] })
	old := f.campaigns.allTargets(noCode.ID)[0]
	old.Outcome = "callback"
	require.NoError(t, f.campaigns.UpdateTarget(context.Background(), &old))
	f.poll()
	require.Len(t, f.tel.dials(), 2)
	tg = f.campaigns.allTargets(noCode.ID)[0]
	assert.Equal(t, domain.TargetPending, tg.Status)
	assert.Empty(t, tg.Outcome)
	code, _ = callOutcome(new(f.call(f.tel.dials()[1].CallID)))
	assert.Empty(t, code)
}
