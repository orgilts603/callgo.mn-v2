package recording

import (
	"context"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
)

// HandleEgressWebhook feeds a LiveKit webhook event to svc. It returns
// handled=false for events other than egress_ended (and when svc is nil), so
// the LiveKit webhook handler can call it first for every event:
//
//	if handled, err := recording.HandleEgressWebhook(ctx, recSvc, ev); handled { return err }
func HandleEgressWebhook(ctx context.Context, svc *Service, ev *lkproto.WebhookEvent) (bool, error) {
	if svc == nil || ev == nil || ev.GetEvent() != livekit.WebhookEgressEnded {
		return false, nil
	}
	if room, id, key, size, dur, ok := livekit.ParseEgressEnded(ev); ok {
		return true, svc.OnEgressEnded(ctx, room, id, key, size, dur)
	}
	if room, id, reason, failed := livekit.EgressFailure(ev); failed {
		return true, svc.OnEgressFailed(ctx, room, id, reason)
	}
	return true, nil
}

// ObserveBus wraps next so that call.answered events start recordings and
// call.ended events stop running egresses, without touching the publishers.
// Events are forwarded to next unchanged first; the recording work runs in
// the background (detached from the publisher's context, bounded by a
// timeout). Use Wait on shutdown. The Service itself must be constructed
// with the unwrapped bus.
func (s *Service) ObserveBus(next domain.EventBus) domain.EventBus {
	return observedBus{next: next, s: s}
}

// Wait blocks until background work started by ObserveBus has finished.
func (s *Service) Wait() { s.bg.Wait() }

type observedBus struct {
	next domain.EventBus
	s    *Service
}

func (b observedBus) Publish(ctx context.Context, ev domain.Event) {
	if b.next != nil {
		b.next.Publish(ctx, ev)
	}
	if ev.Type != domain.EventCallAnswered && ev.Type != domain.EventCallEnded {
		return
	}
	call := callFromPayload(ev.Payload)
	if call == nil || call.ID == uuid.Nil {
		return
	}
	c := *call
	b.s.bg.Add(1)
	go func() {
		defer b.s.bg.Done()
		defer func() {
			if r := recover(); r != nil {
				b.s.log.Error().Interface("panic", r).Msg("recording: bus hook panicked")
			}
		}()
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookTimeout)
		defer cancel()
		// The payload snapshot may predate the stored recording state.
		if fresh, err := b.s.calls.GetCall(hctx, c.ID); err == nil && fresh != nil {
			c = *fresh
		}
		switch ev.Type {
		case domain.EventCallAnswered:
			if err := b.s.OnCallAnswered(hctx, &c); err != nil {
				b.s.log.Error().Err(err).Str("callId", c.ID.String()).Msg("recording: start on call.answered")
			}
		case domain.EventCallEnded:
			b.s.OnCallEnded(hctx, &c)
		}
	}()
}

// callFromPayload extracts the call of a {"call": Call} payload.
func callFromPayload(p any) *domain.Call {
	switch v := p.(type) {
	case map[string]any:
		return callFromPayload(v["call"])
	case *domain.Call:
		return v
	case domain.Call:
		return &v
	}
	return nil
}
