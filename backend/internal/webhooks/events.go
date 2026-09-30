// Package webhooks delivers org events to customer HTTP endpoints: signed
// POSTs with retries, delivery logs and automatic disabling of dead endpoints.
package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Event types introduced by the SaaS sprint (docs/EVENTS.md "SaaS additions").
const (
	EventBillingUpdated    domain.EventType = "billing.updated"
	EventQuotaWarning      domain.EventType = "quota.warning"
	EventWebhookFailed     domain.EventType = "webhook.failed"
	EventCallbackScheduled domain.EventType = "callback.scheduled"
)

// Wildcard subscribes a webhook to every deliverable event.
const Wildcard = "*"

// Header names of an outbound delivery.
const (
	HeaderEvent     = "X-CallGo-Event"
	HeaderDelivery  = "X-CallGo-Delivery"
	HeaderTimestamp = "X-CallGo-Timestamp"
	HeaderSignature = "X-CallGo-Signature"
)

const signaturePrefix = "sha256="

// knownEvents are the event types a webhook may subscribe to. High-frequency
// browser-only events (transcript.partial, agent.state) are never delivered.
var knownEvents = []domain.EventType{
	domain.EventCallStarted,
	domain.EventCallRinging,
	domain.EventCallAnswered,
	domain.EventCallEnded,
	domain.EventCallUpdated,
	domain.EventTranscriptFinal,
	domain.EventCampaignProgress,
	domain.EventLexiconUpdated,
	domain.EventSystem,
	EventBillingUpdated,
	EventQuotaWarning,
	EventWebhookFailed,
	EventCallbackScheduled,
}

// KnownEvents returns the subscribable event types.
func KnownEvents() []domain.EventType { return slices.Clone(knownEvents) }

// Deliverable reports whether events of type t are ever sent to webhooks.
func Deliverable(t domain.EventType) bool {
	return slices.Contains(knownEvents, t)
}

// ValidateEvents checks that every entry is a known event type or "*".
// An empty list is invalid.
func ValidateEvents(events []string) error {
	if len(events) == 0 {
		return fmt.Errorf("%w: events must not be empty", domain.ErrInvalid)
	}
	for _, e := range events {
		if e == Wildcard {
			continue
		}
		if !Deliverable(domain.EventType(e)) {
			return fmt.Errorf("%w: unknown event %q", domain.ErrInvalid, e)
		}
	}
	return nil
}

// Sign returns "sha256=<hex hmac(secret, timestamp + "." + body)>".
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	return signaturePrefix + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a X-CallGo-Signature header value in constant time. Receivers
// should additionally reject stale timestamps to prevent replays.
func Verify(secret, timestamp string, body []byte, signature string) bool {
	if !strings.HasPrefix(signature, signaturePrefix) {
		return false
	}
	want := Sign(secret, timestamp, body)
	return hmac.Equal([]byte(want), []byte(signature))
}

// NewSecret returns a fresh signing secret ("whsec_" + 64 hex chars) and the
// hint stored next to it for display.
func NewSecret() (secret, hint string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate webhook secret: %w", err)
	}
	secret = "whsec_" + hex.EncodeToString(b)
	return secret, SecretHint(secret), nil
}

// SecretHint is the display form of a secret: "whsec_…abcd".
func SecretHint(secret string) string {
	if len(secret) <= 4 {
		return "…"
	}
	return "whsec_…" + secret[len(secret)-4:]
}
