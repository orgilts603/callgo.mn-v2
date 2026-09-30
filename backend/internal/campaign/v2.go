package campaign

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// ---------------------------------------------------------------------------
// Calling window and pacing

// inWindow reports whether the campaign may start dials at now. Outside the
// window it logs at debug level at most once per windowLogEvery.
func (e *Engine) inWindow(c *domain.Campaign, now time.Time) bool {
	s := c.Schedule
	if s.StartTime == "" && s.EndTime == "" && len(s.Weekdays) == 0 {
		return true
	}
	if inWindowIn(s, now, e.location(s.Timezone)) {
		return true
	}
	e.mu.Lock()
	last, logged := e.windowLog[c.ID]
	due := !logged || now.Sub(last) >= windowLogEvery
	if due {
		e.windowLog[c.ID] = now
	}
	e.mu.Unlock()
	if due {
		e.log.Debug().
			Str("campaignId", c.ID.String()).
			Str("timezone", s.Timezone).
			Str("startTime", s.StartTime).
			Str("endTime", s.EndTime).
			Msg("campaign outside its calling window; not dialing")
	}
	return false
}

// location resolves (and caches) a schedule time zone. An unknown zone is
// logged once and treated as UTC.
func (e *Engine) location(name string) *time.Location {
	e.mu.Lock()
	loc, ok := e.locations[name]
	e.mu.Unlock()
	if ok {
		return loc
	}
	loc, err := loadLocation(name)
	if err != nil {
		e.log.Warn().Err(err).Str("timezone", name).Msg("invalid campaign schedule time zone; using UTC")
	}
	e.mu.Lock()
	e.locations[name] = loc
	e.mu.Unlock()
	return loc
}

// paceAvailable returns how many dials the campaign's token bucket allows now.
func (e *Engine) paceAvailable(campaignID uuid.UUID, pace int, now time.Time) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.buckets[campaignID]
	if !ok {
		b = newTokenBucket(pace, now)
		e.buckets[campaignID] = b
	}
	return b.available(pace, now)
}

// paceTake consumes n tokens of the campaign's bucket (no-op without pacing).
func (e *Engine) paceTake(campaignID uuid.UUID, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if b, ok := e.buckets[campaignID]; ok {
		b.take(n)
	}
}

func (e *Engine) dropBucket(campaignID uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.buckets, campaignID)
}

// pruneSchedulingState forgets pacing / window-log state of campaigns that
// are no longer running.
func (e *Engine) pruneSchedulingState(running []domain.Campaign) {
	ids := make(map[uuid.UUID]struct{}, len(running))
	for i := range running {
		ids[running[i].ID] = struct{}{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for id := range e.buckets {
		if _, ok := ids[id]; !ok {
			delete(e.buckets, id)
		}
	}
	for id := range e.windowLog {
		if _, ok := ids[id]; !ok {
			delete(e.windowLog, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Dry run

// pauseAfterDryRun adds dialed to the campaign's DryRunDialed and pauses the
// running campaign once its dry-run limit is reached, publishing
// campaign.progress.
func (e *Engine) pauseAfterDryRun(ctx context.Context, campaignID uuid.UUID, dialed int) {
	log := e.log.With().Str("campaignId", campaignID.String()).Logger()
	e.stateMu.Lock()
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		e.stateMu.Unlock()
		log.Error().Err(err).Msg("load campaign for dry-run accounting")
		return
	}
	if c.DryRunLimit <= 0 {
		e.stateMu.Unlock()
		return
	}
	c.DryRunDialed += dialed
	paused := false
	if c.DryRunDialed >= c.DryRunLimit && c.Status == domain.CampaignRunning {
		c.Status = domain.CampaignPaused
		paused = true
	}
	if dialed == 0 && !paused {
		e.stateMu.Unlock()
		return
	}
	c.UpdatedAt = e.now()
	err = e.campaigns.UpdateCampaign(ctx, c)
	e.stateMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("update campaign dry-run state")
		return
	}
	if paused {
		e.emit(ctx, e.progressEvent(c, nil))
		log.Info().Int("dryRunLimit", c.DryRunLimit).Msg("dry-run limit reached; campaign paused")
	}
}

// ---------------------------------------------------------------------------
// Do-not-call

// skipDoNotCall marks claimed targets whose number is on the organisation's
// do-not-call list as skipped and returns the ones to dial. When the list
// cannot be checked, every claimed target is given back (pending) and an
// error is returned: nothing is dialled unverified.
func (e *Engine) skipDoNotCall(ctx context.Context, c *domain.Campaign, claimed []domain.CampaignTarget) ([]domain.CampaignTarget, error) {
	if e.dnc == nil || len(claimed) == 0 {
		return claimed, nil
	}
	phones := make([]string, len(claimed))
	for i := range claimed {
		phones[i] = claimed[i].Phone
	}
	listed, err := e.dnc.FilterDoNotCall(ctx, c.OrgID, phones)
	if err != nil {
		e.unclaim(context.WithoutCancel(ctx), claimed)
		return nil, fmt.Errorf("check do-not-call list: %w", err)
	}
	dial := claimed[:0:0]
	var skip []domain.CampaignTarget
	for _, t := range claimed {
		if listed[t.Phone] {
			skip = append(skip, t)
		} else {
			dial = append(dial, t)
		}
	}
	if len(skip) > 0 {
		e.markSkipped(ctx, c.ID, skip)
	}
	return dial, nil
}

// unclaim puts claimed but undialled targets back to pending without
// touching their attempts.
func (e *Engine) unclaim(ctx context.Context, targets []domain.CampaignTarget) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	for i := range targets {
		t := targets[i]
		t.Status = domain.TargetPending
		t.UpdatedAt = e.now()
		if err := e.campaigns.UpdateTarget(ctx, &t); err != nil {
			e.log.Error().Err(err).Str("targetId", t.ID.String()).Msg("release claimed target")
		}
	}
}

// markSkipped settles do-not-call targets: status skipped, LastError
// "do_not_call", campaign.Skipped incremented.
func (e *Engine) markSkipped(ctx context.Context, campaignID uuid.UUID, targets []domain.CampaignTarget) {
	log := e.log.With().Str("campaignId", campaignID.String()).Logger()
	var events []domain.Event
	e.stateMu.Lock()
	now := e.now()
	skipped := make([]domain.CampaignTarget, 0, len(targets))
	for i := range targets {
		t := targets[i]
		t.Status = domain.TargetSkipped
		t.LastError = lastErrorDoNotCall
		t.NextTryAt = nil
		t.UpdatedAt = now
		if err := e.campaigns.UpdateTarget(ctx, &t); err != nil {
			log.Error().Err(err).Str("targetId", t.ID.String()).Msg("skip do-not-call target")
			continue
		}
		log.Info().Str("targetId", t.ID.String()).Msg("target on do-not-call list; skipped")
		skipped = append(skipped, t)
	}
	if len(skipped) == 0 {
		e.stateMu.Unlock()
		return
	}
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err == nil {
		c.Skipped += len(skipped)
		e.completeIfDone(ctx, c)
		c.UpdatedAt = now
		err = e.campaigns.UpdateCampaign(ctx, c)
	}
	e.stateMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("update skipped counter")
		return
	}
	for i := range skipped {
		events = append(events, e.progressEvent(c, &skipped[i]))
	}
	e.emit(ctx, events...)
}

// ---------------------------------------------------------------------------
// Outcomes and completion

// findOutcome looks up an outcome code among the campaign's outcomes.
func findOutcome(outcomes []domain.CampaignOutcome, code string) (domain.CampaignOutcome, bool) {
	if code == "" {
		return domain.CampaignOutcome{}, false
	}
	for _, o := range outcomes {
		if o.Code == code {
			return o, true
		}
	}
	return domain.CampaignOutcome{}, false
}

// noContactCode is the outcome recorded for a never-answered call:
// "no_contact" when the campaign defines it, else "".
func noContactCode(outcomes []domain.CampaignOutcome) string {
	if _, ok := findOutcome(outcomes, outcomeNoContact); ok {
		return outcomeNoContact
	}
	return ""
}

// completeIfDone marks a running / paused campaign completed when every
// target is settled (done, failed or skipped). The caller holds stateMu and
// persists c.
func (e *Engine) completeIfDone(ctx context.Context, c *domain.Campaign) {
	if c.Completed+c.Failed+c.Skipped < c.Total {
		return
	}
	if c.Status != domain.CampaignRunning && c.Status != domain.CampaignPaused {
		return
	}
	open, err := e.hasOpenTargets(ctx, c.ID)
	if err != nil {
		e.log.Warn().Err(err).Str("campaignId", c.ID.String()).Msg("check campaign completion")
		return
	}
	if !open {
		c.Status = domain.CampaignCompleted
		e.log.Info().Str("campaignId", c.ID.String()).Msg("campaign completed")
	}
}
