package campaign

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type outcomeKind int

const (
	// outcomeDone: the target was reached (completed / voicemail).
	outcomeDone outcomeKind = iota
	// outcomeRetry: the attempt failed; retry after RetryBackoff while
	// Attempts < MaxAttempts, otherwise mark the target failed.
	outcomeRetry
	// outcomeRequeue: put the target back to pending after delay without
	// judging the attempt (infrastructure problem, shutdown, orphan).
	outcomeRequeue
)

type outcome struct {
	kind outcomeKind
	// reason is the call end reason / status (no_answer, busy, failed, ...).
	reason string
	// detail is a human-readable error stored in CampaignTarget.LastError.
	detail string
	// delay before a requeued target becomes due again (outcomeRequeue).
	delay time.Duration
	// refund gives back the attempt counted when the call was created
	// (outcomeRequeue).
	refund bool
}

// outcomeFromCall derives the target outcome from a terminal call.
func outcomeFromCall(call *domain.Call) outcome {
	if call.Status == domain.StatusCompleted || call.Status == domain.StatusVoicemail || call.EndReason == "voicemail" {
		return outcome{kind: outcomeDone, reason: call.EndReason}
	}
	reason := call.EndReason
	if reason == "" {
		reason = string(call.Status)
	}
	return outcome{kind: outcomeRetry, reason: reason, detail: reason}
}

// settle applies an outcome to a target in "calling" state and updates the
// campaign counters / status. The caller must hold stateMu and must own the
// target (removed its slot or verified its state in the repository). It
// returns the campaign as stored after the transition.
func (e *Engine) settle(ctx context.Context, campaignID uuid.UUID, t *domain.CampaignTarget, o outcome) (*domain.Campaign, error) {
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("get campaign: %w", err)
	}
	now := e.now()
	t.UpdatedAt = now
	final := false
	detail := o.detail
	if detail == "" {
		detail = o.reason
	}
	switch o.kind {
	case outcomeDone:
		t.Status = domain.TargetDone
		t.NextTryAt = nil
		t.LastError = ""
		c.Completed++
		final = true
	case outcomeRequeue:
		t.Status = domain.TargetPending
		if o.refund && t.Attempts > 0 {
			t.Attempts--
		}
		next := now.Add(o.delay)
		t.NextTryAt = &next
		t.LastError = detail
	default:
		if t.Attempts < max(c.MaxAttempts, 1) {
			t.Status = domain.TargetPending
			next := now.Add(e.opts.RetryBackoff)
			t.NextTryAt = &next
			t.LastError = detail
		} else {
			t.Status = domain.TargetFailed
			t.NextTryAt = nil
			t.LastError = detail
			c.Failed++
			final = true
		}
	}
	if err := e.campaigns.UpdateTarget(ctx, t); err != nil {
		return nil, fmt.Errorf("update target: %w", err)
	}
	if !final {
		return c, nil
	}
	if c.Completed+c.Failed >= c.Total && (c.Status == domain.CampaignRunning || c.Status == domain.CampaignPaused) {
		open, err := e.hasOpenTargets(ctx, c.ID)
		if err != nil {
			e.log.Warn().Err(err).Str("campaignId", c.ID.String()).Msg("check campaign completion")
		} else if !open {
			c.Status = domain.CampaignCompleted
			e.log.Info().Str("campaignId", c.ID.String()).Msg("campaign completed")
		}
	}
	c.UpdatedAt = now
	if err := e.campaigns.UpdateCampaign(ctx, c); err != nil {
		return nil, fmt.Errorf("update campaign: %w", err)
	}
	return c, nil
}

// OnCallEnded settles the campaign target of a finished outbound call. The
// HTTP layer calls it after persisting the terminal call (agent call.ended,
// LiveKit room_finished, manual hangup). Non-campaign or non-terminal calls
// are ignored, and repeated calls for the same call are no-ops.
func (e *Engine) OnCallEnded(ctx context.Context, call *domain.Call) {
	if call == nil || call.CampaignID == nil {
		return
	}
	log := e.log.With().Str("callId", call.ID.String()).Str("campaignId", call.CampaignID.String()).Logger()
	if !call.Status.IsTerminal() {
		log.Debug().Str("status", string(call.Status)).Msg("OnCallEnded with non-terminal call; ignored")
		return
	}
	campaignID := *call.CampaignID
	targetID := targetIDFromMetadata(call.Metadata)

	e.stateMu.Lock()
	var target *domain.CampaignTarget
	if s, ok := e.takeSlotForCall(targetID, call.ID); ok {
		t := s.target
		target = &t
	} else {
		t, err := e.findCallingTarget(ctx, campaignID, targetID, call.ID)
		if err != nil {
			e.stateMu.Unlock()
			log.Error().Err(err).Msg("find campaign target for ended call")
			return
		}
		target = t
	}
	if target == nil {
		// Already settled (or not a dialer call): idempotent no-op.
		e.stateMu.Unlock()
		log.Debug().Msg("no calling target for ended call")
		return
	}
	c, err := e.settle(ctx, campaignID, target, outcomeFromCall(call))
	e.stateMu.Unlock()
	if err != nil {
		log.Error().Err(err).Msg("settle campaign target")
		return
	}
	e.emit(ctx, e.progressEvent(c, target))
}

func (e *Engine) takeSlotForCall(targetID, callID uuid.UUID) (*slot, bool) {
	if targetID != uuid.Nil {
		return e.takeSlot(targetID, callID)
	}
	e.mu.Lock()
	var found uuid.UUID
	for id, s := range e.active {
		if s.callID == callID {
			found = id
			break
		}
	}
	e.mu.Unlock()
	if found == uuid.Nil {
		return nil, false
	}
	return e.takeSlot(found, callID)
}

// findCallingTarget scans the campaign's targets for the one in "calling"
// state linked to callID. It returns nil when there is none.
func (e *Engine) findCallingTarget(ctx context.Context, campaignID, targetID, callID uuid.UUID) (*domain.CampaignTarget, error) {
	var found *domain.CampaignTarget
	err := e.scanTargets(ctx, campaignID, func(t *domain.CampaignTarget) bool {
		if t.ID == targetID || (t.CallID != nil && *t.CallID == callID) {
			if t.Status == domain.TargetCalling && t.CallID != nil && *t.CallID == callID {
				cp := *t
				found = &cp
			}
			return false
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func targetIDFromMetadata(md map[string]any) uuid.UUID {
	switch v := md["targetId"].(type) {
	case string:
		if id, err := uuid.Parse(v); err == nil {
			return id
		}
	case uuid.UUID:
		return v
	}
	return uuid.Nil
}

// scanTargets pages through all targets of a campaign; fn returns false to stop.
func (e *Engine) scanTargets(ctx context.Context, campaignID uuid.UUID, fn func(*domain.CampaignTarget) bool) error {
	for offset := 0; ; {
		page, total, err := e.campaigns.ListTargets(ctx, campaignID, targetPageSize, offset)
		if err != nil {
			return fmt.Errorf("list targets: %w", err)
		}
		for i := range page {
			if !fn(&page[i]) {
				return nil
			}
		}
		offset += len(page)
		if len(page) == 0 || offset >= total {
			return nil
		}
	}
}

func (e *Engine) hasOpenTargets(ctx context.Context, campaignID uuid.UUID) (bool, error) {
	open := false
	err := e.scanTargets(ctx, campaignID, func(t *domain.CampaignTarget) bool {
		if t.Status == domain.TargetPending || t.Status == domain.TargetCalling {
			open = true
			return false
		}
		return true
	})
	return open, err
}

// maybeSync recomputes an idle campaign's counters at most once per sync
// interval and completes it when no pending/calling targets remain.
func (e *Engine) maybeSync(ctx context.Context, campaignID uuid.UUID) {
	now := e.now()
	e.mu.Lock()
	last, ok := e.lastSync[campaignID]
	due := !ok || now.Sub(last) >= e.syncInterval()
	if due {
		e.lastSync[campaignID] = now
	}
	e.mu.Unlock()
	if !due {
		return
	}
	if err := e.syncCampaign(ctx, campaignID); err != nil && ctx.Err() == nil {
		e.log.Error().Err(err).Str("campaignId", campaignID.String()).Msg("sync campaign counters")
	}
}

func (e *Engine) syncCampaign(ctx context.Context, campaignID uuid.UUID) error {
	e.stateMu.Lock()
	var total, done, failed, open int
	err := e.scanTargets(ctx, campaignID, func(t *domain.CampaignTarget) bool {
		total++
		switch t.Status {
		case domain.TargetDone:
			done++
		case domain.TargetFailed:
			failed++
		default:
			open++
		}
		return true
	})
	if err != nil {
		e.stateMu.Unlock()
		return err
	}
	c, err := e.campaigns.GetCampaign(ctx, campaignID)
	if err != nil {
		e.stateMu.Unlock()
		return fmt.Errorf("get campaign: %w", err)
	}
	changed := c.Total != total || c.Completed != done || c.Failed != failed
	c.Total, c.Completed, c.Failed = total, done, failed
	if open == 0 && total > 0 && (c.Status == domain.CampaignRunning || c.Status == domain.CampaignPaused) {
		c.Status = domain.CampaignCompleted
		changed = true
		e.log.Info().Str("campaignId", campaignID.String()).Msg("campaign completed")
	}
	if !changed {
		e.stateMu.Unlock()
		return nil
	}
	c.UpdatedAt = e.now()
	err = e.campaigns.UpdateCampaign(ctx, c)
	e.stateMu.Unlock()
	if err != nil {
		return fmt.Errorf("update campaign: %w", err)
	}
	e.emit(ctx, e.progressEvent(c, nil))
	return nil
}

// reconcile repairs targets of running campaigns left in "calling" by a
// previous process (crash / restart).
func (e *Engine) reconcile(ctx context.Context) error {
	running, err := e.campaigns.ListRunningCampaigns(ctx)
	if err != nil {
		return fmt.Errorf("list running campaigns: %w", err)
	}
	var errs []error
	for i := range running {
		if err := e.reconcileCampaign(ctx, running[i].ID); err != nil {
			errs = append(errs, fmt.Errorf("campaign %s: %w", running[i].ID, err))
		}
	}
	return errors.Join(errs...)
}

// reconcileCampaign handles every "calling" target not owned by this engine:
//   - its call is terminal → settle it from the call outcome;
//   - it has no call / the call row is missing and the target has not been
//     touched for 2×MaxCallDuration → requeue (attempt not counted);
//   - its call is still non-terminal after 2×MaxCallDuration → close the
//     call as failed (max_duration) and retry the target.
//
// Targets younger than that are left alone: the call may still be live in
// LiveKit and will be settled by OnCallEnded.
func (e *Engine) reconcileCampaign(ctx context.Context, campaignID uuid.UUID) error {
	var stuck []domain.CampaignTarget
	if err := e.scanTargets(ctx, campaignID, func(t *domain.CampaignTarget) bool {
		if t.Status == domain.TargetCalling {
			stuck = append(stuck, *t)
		}
		return true
	}); err != nil {
		return err
	}
	if len(stuck) == 0 {
		return nil
	}

	var events []domain.Event
	var errs []error
	e.stateMu.Lock()
	for i := range stuck {
		t := stuck[i]
		e.mu.Lock()
		_, owned := e.active[t.ID]
		e.mu.Unlock()
		if owned {
			continue
		}
		evs, err := e.resolveOrphan(ctx, campaignID, &t)
		if err != nil {
			errs = append(errs, fmt.Errorf("target %s: %w", t.ID, err))
			continue
		}
		events = append(events, evs...)
	}
	e.stateMu.Unlock()
	e.emit(ctx, events...)
	return errors.Join(errs...)
}

// resolveOrphan implements the reconcile rules for one target. Caller holds
// stateMu and owns the target.
func (e *Engine) resolveOrphan(ctx context.Context, campaignID uuid.UUID, t *domain.CampaignTarget) ([]domain.Event, error) {
	now := e.now()
	stale := e.staleAfter()
	log := e.log.With().Str("campaignId", campaignID.String()).Str("targetId", t.ID.String()).Logger()

	requeue := func(refund bool) ([]domain.Event, error) {
		if now.Sub(t.UpdatedAt) <= stale {
			return nil, nil
		}
		log.Warn().Msg("requeueing orphaned campaign target")
		c, err := e.settle(ctx, campaignID, t, outcome{kind: outcomeRequeue, refund: refund, reason: "orphaned", detail: "requeued after dialer restart"})
		if err != nil {
			return nil, err
		}
		return []domain.Event{e.progressEvent(c, t)}, nil
	}

	if t.CallID == nil {
		return requeue(false)
	}
	call, err := e.calls.GetCall(ctx, *t.CallID)
	if errors.Is(err, domain.ErrNotFound) {
		return requeue(true)
	}
	if err != nil {
		return nil, fmt.Errorf("get call: %w", err)
	}
	return e.settleFromCall(ctx, campaignID, t, call)
}

// settleFromCall settles a target whose call is terminal, or expires a call
// that has been open for more than 2×MaxCallDuration. Caller holds stateMu.
func (e *Engine) settleFromCall(ctx context.Context, campaignID uuid.UUID, t *domain.CampaignTarget, call *domain.Call) ([]domain.Event, error) {
	var events []domain.Event
	o := outcomeFromCall(call)
	if !call.Status.IsTerminal() {
		opened := call.StartedAt
		if call.AnsweredAt != nil {
			opened = *call.AnsweredAt
		}
		if e.now().Sub(opened) <= e.staleAfter() {
			return nil, nil
		}
		e.log.Warn().Str("callId", call.ID.String()).Msg("expiring campaign call open for more than 2×MaxCallDuration")
		e.closeCall(call, domain.StatusFailed, "max_duration")
		if err := e.calls.UpdateCall(ctx, call); err != nil {
			return nil, fmt.Errorf("expire call: %w", err)
		}
		events = append(events, e.endedEvent(call))
		o = outcome{kind: outcomeRetry, reason: "max_duration", detail: "call exceeded max duration without end report"}
	}
	c, err := e.settle(ctx, campaignID, t, o)
	if err != nil {
		return events, err
	}
	return append(events, e.progressEvent(c, t)), nil
}

// sweepStale settles answered calls held by this engine whose end was never
// reported (agent crash, lost webhook) after 2×MaxCallDuration.
func (e *Engine) sweepStale(ctx context.Context) {
	now := e.now()
	type candidate struct {
		targetID, callID, campaignID uuid.UUID
	}
	var cands []candidate
	e.mu.Lock()
	for id, s := range e.active {
		if !s.dialing && s.callID != uuid.Nil && now.Sub(s.since) > e.staleAfter() {
			cands = append(cands, candidate{targetID: id, callID: s.callID, campaignID: s.campaignID})
		}
	}
	e.mu.Unlock()

	for _, cd := range cands {
		call, err := e.calls.GetCall(ctx, cd.callID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			e.log.Error().Err(err).Str("callId", cd.callID.String()).Msg("load stale campaign call")
			continue
		}
		e.stateMu.Lock()
		s, ok := e.takeSlot(cd.targetID, cd.callID)
		if !ok {
			e.stateMu.Unlock()
			continue
		}
		t := s.target
		var events []domain.Event
		if call == nil {
			var c *domain.Campaign
			c, err = e.settle(ctx, cd.campaignID, &t, outcome{kind: outcomeRequeue, refund: true, reason: "orphaned", detail: "call row missing"})
			if c != nil {
				events = append(events, e.progressEvent(c, &t))
			}
		} else {
			if !call.Status.IsTerminal() && call.AnsweredAt == nil {
				call.AnsweredAt = &s.since
			}
			events, err = e.settleFromCall(ctx, cd.campaignID, &t, call)
			if err == nil && len(events) == 0 {
				// Not stale by the call's own clock: keep holding the slot.
				e.mu.Lock()
				e.active[cd.targetID] = s
				e.mu.Unlock()
			}
		}
		e.stateMu.Unlock()
		if err != nil {
			e.log.Error().Err(err).Str("callId", cd.callID.String()).Msg("settle stale campaign call")
		}
		e.emit(ctx, events...)
	}
}
