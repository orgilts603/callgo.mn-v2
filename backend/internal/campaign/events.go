package campaign

import (
	"context"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func (e *Engine) event(typ domain.EventType, orgID uuid.UUID, callID *uuid.UUID, payload any) domain.Event {
	return domain.Event{
		ID:      uuid.NewString(),
		Type:    typ,
		OrgID:   orgID,
		CallID:  callID,
		At:      e.now(),
		Payload: payload,
	}
}

// callEvent builds call.started / call.ringing / call.answered ({"call": Call}).
func (e *Engine) callEvent(typ domain.EventType, call *domain.Call) domain.Event {
	cp := *call
	id := cp.ID
	return e.event(typ, cp.OrgID, &id, map[string]any{"call": cp})
}

// endedEvent builds call.ended for a call the engine closed itself.
func (e *Engine) endedEvent(call *domain.Call) domain.Event {
	cp := *call
	id := cp.ID
	return e.event(domain.EventCallEnded, cp.OrgID, &id, map[string]any{
		"call":        cp,
		"endReason":   cp.EndReason,
		"durationSec": cp.DurationSec,
	})
}

// progressEvent builds campaign.progress ({"campaign": Campaign, "target": CampaignTarget|null}).
func (e *Engine) progressEvent(c *domain.Campaign, t *domain.CampaignTarget) domain.Event {
	var target *domain.CampaignTarget
	var callID *uuid.UUID
	if t != nil {
		cp := *t
		target = &cp
		if cp.CallID != nil {
			id := *cp.CallID
			callID = &id
		}
	}
	return e.event(domain.EventCampaignProgress, c.OrgID, callID, map[string]any{
		"campaign": *c,
		"target":   target,
	})
}

func (e *Engine) emit(ctx context.Context, events ...domain.Event) {
	if e.bus == nil {
		return
	}
	for _, ev := range events {
		e.bus.Publish(ctx, ev)
	}
}
