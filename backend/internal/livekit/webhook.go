package livekit

import (
	"fmt"
	"net/http"

	"github.com/livekit/protocol/auth"
	lkproto "github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Webhook event names (livekit.WebhookEvent.Event).
const (
	WebhookRoomStarted       = webhook.EventRoomStarted
	WebhookRoomFinished      = webhook.EventRoomFinished
	WebhookParticipantJoined = webhook.EventParticipantJoined
	WebhookParticipantLeft   = webhook.EventParticipantLeft
	WebhookEgressEnded       = webhook.EventEgressEnded
)

// ParseWebhook reads r's body, verifies the LiveKit signature (JWT in the
// Authorization header signed with key/secret, carrying the body's SHA-256)
// and decodes the event. Signature problems wrap domain.ErrUnauthorized and
// malformed bodies wrap domain.ErrInvalid. The body is consumed and closed.
func ParseWebhook(r *http.Request, key, secret string) (*lkproto.WebhookEvent, error) {
	data, err := webhook.Receive(r, auth.NewSimpleKeyProvider(key, secret))
	if err != nil {
		return nil, fmt.Errorf("%w: livekit webhook: %v", domain.ErrUnauthorized, err)
	}
	var ev lkproto.WebhookEvent
	opts := protojson.UnmarshalOptions{DiscardUnknown: true, AllowPartial: true}
	if err := opts.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("%w: livekit webhook body: %v", domain.ErrInvalid, err)
	}
	return &ev, nil
}
