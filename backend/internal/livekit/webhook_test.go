package livekit

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	lkproto "github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// signedRequest builds a webhook request exactly like livekit-server's
// URLNotifier: JWT (key/secret) carrying the base64 SHA-256 of the body.
func signedRequest(t *testing.T, body []byte, key, secret string) *http.Request {
	t.Helper()
	sum := sha256.Sum256(body)
	token, err := auth.NewAccessToken(key, secret).
		SetValidFor(5 * time.Minute).
		SetSha256(base64.StdEncoding.EncodeToString(sum[:])).
		ToJWT()
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/api/livekit/webhook", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/webhook+json")
	r.Header.Set("Authorization", token)
	return r
}

func webhookBody(t *testing.T) []byte {
	t.Helper()
	ev := &lkproto.WebhookEvent{
		Event: WebhookParticipantJoined,
		Id:    "EV_1",
		Room:  &lkproto.Room{Name: "call_+97699112233_abc", Sid: "RM_1"},
		Participant: &lkproto.ParticipantInfo{
			Identity: "sip_+97699112233",
			Kind:     lkproto.ParticipantInfo_SIP,
			Attributes: map[string]string{
				"sip.phoneNumber":      "+97699112233",
				"sip.trunkPhoneNumber": "+97677001234",
				"sip.callID":           "SCL_1",
			},
		},
		CreatedAt: time.Now().Unix(),
	}
	b, err := protojson.Marshal(ev)
	require.NoError(t, err)
	return b
}

func TestParseWebhookRoundTrip(t *testing.T) {
	body := webhookBody(t)
	ev, err := ParseWebhook(signedRequest(t, body, "key", "secret"), "key", "secret")
	require.NoError(t, err)
	assert.Equal(t, WebhookParticipantJoined, ev.GetEvent())
	assert.Equal(t, "call_+97699112233_abc", ev.GetRoom().GetName())
	from, to, callID := SIPAttributes(ev.GetParticipant())
	assert.Equal(t, "+97699112233", from)
	assert.Equal(t, "+97677001234", to)
	assert.Equal(t, "SCL_1", callID)

	c, err := NewClient(Config{URL: "http://lk", APIKey: "key", APISecret: "secret"}, testLogger())
	require.NoError(t, err)
	ev, err = c.ParseWebhook(signedRequest(t, body, "key", "secret"))
	require.NoError(t, err)
	assert.Equal(t, "EV_1", ev.GetId())
}

func TestParseWebhookRejects(t *testing.T) {
	body := webhookBody(t)

	// Wrong secret.
	_, err := ParseWebhook(signedRequest(t, body, "key", "other-secret"), "key", "secret")
	require.ErrorIs(t, err, domain.ErrUnauthorized)

	// Unknown key.
	_, err = ParseWebhook(signedRequest(t, body, "other-key", "secret"), "key", "secret")
	require.ErrorIs(t, err, domain.ErrUnauthorized)

	// Tampered body.
	r := signedRequest(t, body, "key", "secret")
	tampered := bytes.Replace(body, []byte("+97699112233"), []byte("+97600000000"), 1)
	r.Body = httpBody(tampered)
	_, err = ParseWebhook(r, "key", "secret")
	require.ErrorIs(t, err, domain.ErrUnauthorized)

	// No Authorization header.
	r = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	_, err = ParseWebhook(r, "key", "secret")
	require.ErrorIs(t, err, domain.ErrUnauthorized)

	// Correctly signed garbage.
	_, err = ParseWebhook(signedRequest(t, []byte("{not json"), "key", "secret"), "key", "secret")
	require.ErrorIs(t, err, domain.ErrInvalid)
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

func httpBody(b []byte) readCloser { return readCloser{bytes.NewReader(b)} }
