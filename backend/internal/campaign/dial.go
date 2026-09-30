package campaign

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// runTarget places one call for a claimed target. It runs in its own
// goroutine; the target's slot stays held until the call is settled (here
// for unanswered calls, in OnCallEnded for answered ones).
func (e *Engine) runTarget(ctx context.Context, c domain.Campaign, sip domain.SIPNumber, profile domain.AgentProfile, t domain.CampaignTarget) {
	defer e.wg.Done()
	// Writes after the dial must survive a shutdown so no target is lost.
	wctx := context.WithoutCancel(ctx)
	log := e.log.With().
		Str("campaignId", c.ID.String()).
		Str("targetId", t.ID.String()).
		Logger()

	claimedAttempts := t.Attempts
	call, err := e.prepareCall(ctx, &c, &sip, &profile, &t)
	if err != nil {
		log.Error().Err(err).Msg("prepare campaign call")
		t.Attempts = claimedAttempts
		e.releaseUnstarted(wctx, c.ID, t, call, err)
		return
	}
	log = log.With().Str("callId", call.ID.String()).Logger()

	call.Status = domain.StatusRinging
	call.UpdatedAt = e.now()
	if err := e.calls.UpdateCall(ctx, call); err != nil {
		log.Error().Err(err).Msg("mark call ringing")
		t.Attempts = claimedAttempts
		e.releaseUnstarted(wctx, c.ID, t, call, err)
		return
	}
	e.emit(ctx, e.callEvent(domain.EventCallRinging, call))

	vars := t.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	req := domain.OutboundCallRequest{
		CallID:       call.ID,
		RoomName:     call.RoomName,
		FromNumber:   sip,
		ToNumber:     t.Phone,
		AgentProfile: profile,
		Metadata: map[string]any{
			"callId":         call.ID.String(),
			"campaignId":     c.ID.String(),
			"targetId":       t.ID.String(),
			"sipNumberId":    sip.ID.String(),
			"agentProfileId": profile.ID.String(),
			"direction":      string(domain.DirectionOutbound),
			"vars":           vars,
		},
		WaitUntilAnswered: true,
		RingTimeout:       e.opts.RingTimeout,
	}
	dialCtx, cancel := context.WithTimeout(ctx, e.opts.RingTimeout+dialGrace)
	res, dialErr := e.tel.Dial(dialCtx, req)
	cancel()
	if res.SIPCallID != "" {
		call.SIPCallID = res.SIPCallID
	}
	if res.ParticipantID != "" {
		call.ParticipantID = res.ParticipantID
	}

	if ctx.Err() != nil {
		// Dialer shutting down: give the attempt back and retry right away
		// on the next start.
		log.Warn().Msg("dialer stopped while dialing; requeueing target")
		e.finishUnanswered(wctx, c.ID, t.ID, call, domain.StatusFailed,
			outcome{kind: outcomeRequeue, refund: true, reason: "dialer shutdown"})
		return
	}
	if dialErr == nil && res.Answered {
		e.onAnswered(wctx, t.ID, call)
		return
	}

	status := classify(res, dialErr)
	reason := string(status)
	detail := res.Error
	if dialErr != nil {
		detail = dialErr.Error()
	}
	if detail == "" {
		detail = reason
	}
	log.Info().Str("status", reason).Str("detail", detail).Msg("campaign call not answered")
	e.finishUnanswered(wctx, c.ID, t.ID, call, status, outcome{
		kind: outcomeRetry, reason: reason, detail: detail,
		setOutcome: true, code: noContactCode(c.Outcomes),
	})
}

// prepareCall resolves the contact, creates the Call row (queued), links it
// to the target (counting the attempt) and publishes call.started. When the
// call row was created but a later step failed, the call is returned along
// with the error.
func (e *Engine) prepareCall(ctx context.Context, c *domain.Campaign, sip *domain.SIPNumber, profile *domain.AgentProfile, t *domain.CampaignTarget) (*domain.Call, error) {
	contactID := t.ContactID
	if contactID == nil {
		id, err := e.resolveContact(ctx, c.OrgID, t)
		if err != nil {
			// A missing contact must not block the call.
			e.log.Warn().Err(err).Str("targetId", t.ID.String()).Msg("resolve contact for campaign target")
		} else {
			contactID = &id
		}
	}

	now := e.now()
	callID := uuid.New()
	campaignID := c.ID
	sipID := sip.ID
	profileID := profile.ID
	vars := t.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	call := &domain.Call{
		ID:             callID,
		OrgID:          c.OrgID,
		ContactID:      contactID,
		CampaignID:     &campaignID,
		SIPNumberID:    &sipID,
		AgentProfileID: &profileID,
		Direction:      domain.DirectionOutbound,
		Status:         domain.StatusQueued,
		FromNumber:     sip.Number,
		ToNumber:       t.Phone,
		RoomName:       "call-" + callID.String(),
		StartedAt:      now,
		Metadata: map[string]any{
			"targetId": t.ID.String(),
			"vars":     vars,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := e.calls.CreateCall(ctx, call); err != nil {
		return nil, fmt.Errorf("create call: %w", err)
	}

	t.Attempts++
	t.CallID = &call.ID
	t.ContactID = contactID
	t.UpdatedAt = now
	if err := e.campaigns.UpdateTarget(ctx, t); err != nil {
		return call, fmt.Errorf("link target to call: %w", err)
	}

	e.mu.Lock()
	if s, ok := e.active[t.ID]; ok {
		s.callID = call.ID
		s.target = *t
	}
	e.mu.Unlock()

	e.emit(ctx, e.callEvent(domain.EventCallStarted, call))
	return call, nil
}

func (e *Engine) resolveContact(ctx context.Context, orgID uuid.UUID, t *domain.CampaignTarget) (uuid.UUID, error) {
	existing, err := e.contacts.GetContactByPhone(ctx, orgID, t.Phone)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, fmt.Errorf("get contact by phone: %w", err)
	}
	now := e.now()
	ct := &domain.Contact{
		ID:        uuid.New(),
		OrgID:     orgID,
		Phone:     t.Phone,
		Name:      t.Name,
		Tags:      []string{},
		Meta:      map[string]string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := e.contacts.UpsertContact(ctx, ct); err != nil {
		return uuid.Nil, fmt.Errorf("upsert contact: %w", err)
	}
	return ct.ID, nil
}

// onAnswered marks the call active. The target stays "calling" and its slot
// stays held until OnCallEnded.
func (e *Engine) onAnswered(ctx context.Context, targetID uuid.UUID, call *domain.Call) {
	e.stateMu.Lock()
	e.mu.Lock()
	s, ok := e.active[targetID]
	if ok && s.callID == call.ID {
		s.dialing = false
		s.since = e.now()
	} else {
		ok = false
	}
	e.mu.Unlock()
	if !ok {
		// OnCallEnded already settled this call.
		e.stateMu.Unlock()
		return
	}
	if cur, err := e.calls.GetCall(ctx, call.ID); err == nil && cur.Status.IsTerminal() {
		// The HTTP layer already closed the call; OnCallEnded will settle it.
		e.stateMu.Unlock()
		return
	}
	now := e.now()
	call.Status = domain.StatusActive
	call.AnsweredAt = &now
	call.UpdatedAt = now
	err := e.calls.UpdateCall(ctx, call)
	e.stateMu.Unlock()
	if err != nil {
		e.log.Error().Err(err).Str("callId", call.ID.String()).Msg("mark call answered")
		return
	}
	e.emit(ctx, e.callEvent(domain.EventCallAnswered, call))
}

// finishUnanswered closes a call that never got answered and settles its
// target. It is a no-op if the target was already settled elsewhere.
func (e *Engine) finishUnanswered(ctx context.Context, campaignID, targetID uuid.UUID, call *domain.Call, status domain.CallStatus, o outcome) {
	var events []domain.Event
	e.stateMu.Lock()
	s, ok := e.takeSlot(targetID, call.ID)
	if !ok {
		e.stateMu.Unlock()
		return
	}
	if cur, err := e.calls.GetCall(ctx, call.ID); err == nil && cur.Status.IsTerminal() {
		// Somebody (webhook / agent) already closed the call: trust it.
		if o.kind != outcomeRequeue {
			o = outcomeFromCall(cur)
		}
	} else {
		e.closeCall(call, status, string(status))
		if o.setOutcome {
			setCallOutcome(call, o.code, o.note)
		}
		if err := e.calls.UpdateCall(ctx, call); err != nil {
			e.log.Error().Err(err).Str("callId", call.ID.String()).Msg("finalize unanswered call")
		}
		events = append(events, e.endedEvent(call))
	}
	t := s.target
	c, err := e.settle(ctx, campaignID, &t, o)
	e.stateMu.Unlock()
	if err != nil {
		e.log.Error().Err(err).Str("targetId", targetID.String()).Msg("settle campaign target")
	}
	if c != nil {
		events = append(events, e.progressEvent(c, &t))
	}
	e.emit(ctx, events...)
}

// releaseUnstarted gives a claimed target back when its call could not be
// set up (infrastructure error). The attempt is not counted; the target is
// retried after RetryBackoff.
func (e *Engine) releaseUnstarted(ctx context.Context, campaignID uuid.UUID, t domain.CampaignTarget, call *domain.Call, cause error) {
	var events []domain.Event
	e.stateMu.Lock()
	if _, ok := e.takeSlot(t.ID, uuid.Nil); !ok {
		e.stateMu.Unlock()
		return
	}
	if call != nil {
		e.closeCall(call, domain.StatusFailed, string(domain.StatusFailed))
		if err := e.calls.UpdateCall(ctx, call); err != nil {
			e.log.Error().Err(err).Str("callId", call.ID.String()).Msg("finalize failed call")
		}
		events = append(events, e.endedEvent(call))
	}
	c, err := e.settle(ctx, campaignID, &t, outcome{
		kind:   outcomeRequeue,
		delay:  e.opts.RetryBackoff,
		reason: string(domain.StatusFailed),
		detail: cause.Error(),
	})
	e.stateMu.Unlock()
	if err != nil {
		e.log.Error().Err(err).Str("targetId", t.ID.String()).Msg("release campaign target")
	}
	if c != nil {
		events = append(events, e.progressEvent(c, &t))
	}
	e.emit(ctx, events...)
}

// closeCall fills the terminal fields of a call that ended without media.
func (e *Engine) closeCall(call *domain.Call, status domain.CallStatus, reason string) {
	now := e.now()
	call.Status = status
	call.EndedAt = &now
	call.DurationSec = 0
	call.EndReason = reason
	call.UpdatedAt = now
}

// takeSlot removes the slot of targetID if it belongs to callID (any call
// when callID is uuid.Nil). Only the caller that removes the slot may settle
// the target, which makes settlement idempotent inside this process.
func (e *Engine) takeSlot(targetID, callID uuid.UUID) (*slot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.active[targetID]
	if !ok || (callID != uuid.Nil && s.callID != callID) {
		return nil, false
	}
	delete(e.active, targetID)
	return s, true
}
