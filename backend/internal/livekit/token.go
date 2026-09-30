package livekit

import (
	"fmt"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultOperatorTokenTTL is the validity of operator (handoff) tokens.
const DefaultOperatorTokenTTL = time.Hour

// OperatorToken issues a LiveKit access token that lets a human operator join
// roomName from the browser: join + publish + subscribe (+ data), with the
// given participant identity, display name and attributes (e.g.
// callgo.role=operator). ttl <= 0 uses DefaultOperatorTokenTTL.
func OperatorToken(apiKey, apiSecret, roomName, identity, name string, attrs map[string]string, ttl time.Duration) (string, error) {
	var missing []string
	if apiKey == "" {
		missing = append(missing, "api key")
	}
	if apiSecret == "" {
		missing = append(missing, "api secret")
	}
	if strings.TrimSpace(roomName) == "" {
		missing = append(missing, "room name")
	}
	if strings.TrimSpace(identity) == "" {
		missing = append(missing, "identity")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("%w: operator token: missing %s", domain.ErrInvalid, strings.Join(missing, ", "))
	}
	if ttl <= 0 {
		ttl = DefaultOperatorTokenTTL
	}
	grant := &auth.VideoGrant{RoomJoin: true, Room: roomName}
	grant.SetCanPublish(true)
	grant.SetCanSubscribe(true)
	grant.SetCanPublishData(true)
	at := auth.NewAccessToken(apiKey, apiSecret).
		SetIdentity(identity).
		SetName(name).
		SetValidFor(ttl).
		SetVideoGrant(grant)
	if len(attrs) > 0 {
		at.SetAttributes(attrs)
	}
	tok, err := at.ToJWT()
	if err != nil {
		return "", fmt.Errorf("livekit: sign operator token: %w", err)
	}
	return tok, nil
}
