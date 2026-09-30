package webhooks

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// TeeBus is a domain.EventBus that forwards every event to Inner and queues
// deliverable ones for the Dispatcher. live.Hub only offers per-org
// subscriptions, so the integrator wires webhooks by handing TeeBus (instead
// of the raw hub) to every component that publishes events. Publish never
// blocks: when the queue is full the webhook copy is dropped and logged.
type TeeBus struct {
	inner domain.EventBus
	queue chan domain.Event
	log   zerolog.Logger
}

const teeQueueSize = 1024

// NewTeeBus returns the bus and starts its forwarding goroutine, which stops
// when ctx is cancelled. Pass the raw hub as the Dispatcher's bus (for
// webhook.failed) and the TeeBus to the rest of the application.
func NewTeeBus(ctx context.Context, inner domain.EventBus, d *Dispatcher, log zerolog.Logger) *TeeBus {
	t := &TeeBus{inner: inner, queue: make(chan domain.Event, teeQueueSize), log: log.With().Str("component", "webhooks.tee").Logger()}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-t.queue:
				if err := d.Enqueue(ctx, ev); err != nil && ctx.Err() == nil {
					t.log.Error().Err(err).Str("type", string(ev.Type)).Msg("enqueue webhook deliveries")
				}
			}
		}
	}()
	return t
}

// Publish implements domain.EventBus.
func (t *TeeBus) Publish(ctx context.Context, ev domain.Event) {
	// Stamp here so browsers and webhook consumers see the same event id.
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	t.inner.Publish(ctx, ev)
	if !Deliverable(ev.Type) {
		return
	}
	select {
	case t.queue <- ev:
	default:
		t.log.Warn().Str("type", string(ev.Type)).Msg("webhook queue full; event not delivered to webhooks")
	}
}
