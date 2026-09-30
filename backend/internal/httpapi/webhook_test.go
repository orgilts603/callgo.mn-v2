package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	lkauth "github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func signedWebhook(t *testing.T, ev *livekit.WebhookEvent, key, secret string) *http.Request {
	t.Helper()
	body, err := protojson.Marshal(ev)
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	tok, err := lkauth.NewAccessToken(key, secret).SetValidFor(5 * time.Minute).SetSha256(base64.StdEncoding.EncodeToString(sum[:])).ToJWT()
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/api/livekit/webhook", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/webhook+json")
	r.Header.Set("Authorization", tok)
	return r
}

func (e *env) webhook(ev *livekit.WebhookEvent) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, signedWebhook(e.t, ev, testLKKey, testLKSecret))
	return w
}

func sipParticipant(phone, trunk string, extra map[string]string) *livekit.ParticipantInfo {
	attrs := map[string]string{attrSIPPhone: phone, attrSIPTrunkPhone: trunk, attrSIPCallID: "SCL_abc"}
	for k, v := range extra {
		attrs[k] = v
	}
	return &livekit.ParticipantInfo{Sid: "PA_1", Identity: "sip_" + phone, Kind: livekit.ParticipantInfo_SIP, Attributes: attrs}
}

func TestWebhookSignature(t *testing.T) {
	e := newEnv(t)
	ev := &livekit.WebhookEvent{Event: "room_started", Room: &livekit.Room{Name: "call-x"}}

	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, signedWebhook(t, ev, testLKKey, "wrong-secret-wrong-secret-wrong-secret"))
	requireErr(t, w, http.StatusUnauthorized, "unauthorized")

	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, signedWebhook(t, ev, "otherKey", testLKSecret))
	requireErr(t, w, http.StatusUnauthorized, "unauthorized")

	// Tampered body.
	r := signedWebhook(t, ev, testLKKey, testLKSecret)
	r.Body = io.NopCloser(strings.NewReader(`{"event":"room_finished","room":{"name":"call-x"}}`))
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	requireErr(t, w, http.StatusUnauthorized, "unauthorized")

	// Unsigned.
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/livekit/webhook", bytes.NewBufferString(`{}`)))
	requireErr(t, w, http.StatusUnauthorized, "unauthorized")

	assert.Equal(t, http.StatusOK, e.webhook(ev).Code)
}

func TestWebhookInboundLifecycle(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	ct := e.seedContact(e.org.ID, "+97699112233", "Caller")
	room := "call-_+97699112233_Xyz"

	// room_started without metadata defers creation.
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "room_started", Room: &livekit.Room{Name: room}}).Code)
	_, err := e.db.GetCallByRoom(context.Background(), room)
	require.ErrorIs(t, err, domain.ErrNotFound)

	// Agent participant is ignored.
	agentP := &livekit.ParticipantInfo{Identity: "agent-1", Kind: livekit.ParticipantInfo_AGENT}
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "participant_joined", Room: &livekit.Room{Name: room}, Participant: agentP}).Code)

	// SIP participant joins → call created + answered.
	sip := sipParticipant("+97699112233", "+97677001234", nil)
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "participant_joined", Room: &livekit.Room{Name: room}, Participant: sip}).Code)
	c, err := e.db.GetCallByRoom(context.Background(), room)
	require.NoError(t, err)
	assert.Equal(t, e.org.ID, c.OrgID)
	assert.Equal(t, domain.DirectionInbound, c.Direction)
	assert.Equal(t, domain.StatusActive, c.Status)
	assert.NotNil(t, c.AnsweredAt)
	assert.Equal(t, "+97699112233", c.FromNumber)
	assert.Equal(t, "+97677001234", c.ToNumber)
	assert.Equal(t, num.ID, *c.SIPNumberID)
	assert.Equal(t, p.ID, *c.AgentProfileID)
	assert.Equal(t, ct.ID, *c.ContactID)
	assert.Equal(t, "sip_+97699112233", c.ParticipantID)
	assert.Equal(t, "SCL_abc", c.SIPCallID)
	assert.Len(t, e.bus.ofType(domain.EventCallStarted), 1)
	assert.Len(t, e.bus.ofType(domain.EventCallAnswered), 1)

	// Customer hangs up.
	sip.DisconnectReason = livekit.DisconnectReason_CLIENT_INITIATED
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "participant_left", Room: &livekit.Room{Name: room}, Participant: sip}).Code)
	got := e.call(c.ID)
	assert.Equal(t, domain.StatusCompleted, got.Status)
	assert.Equal(t, "hangup_customer", got.EndReason)
	assert.NotNil(t, got.EndedAt)
	require.Len(t, e.bus.ofType(domain.EventCallEnded), 1)

	// room_finished after that is a no-op.
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "room_finished", Room: &livekit.Room{Name: room}}).Code)
	assert.Len(t, e.bus.ofType(domain.EventCallEnded), 1)

	// Recording.
	eg := &livekit.EgressInfo{EgressId: "EG_1", RoomName: room, FileResults: []*livekit.FileInfo{{Filename: "rec.ogg", Location: "https://s3/rec.ogg"}}}
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "egress_ended", EgressInfo: eg}).Code)
	assert.Equal(t, "https://s3/rec.ogg", e.call(c.ID).RecordingURL)
	assert.NotEmpty(t, e.bus.ofType(domain.EventCallUpdated))

	// Unknown trunk number → not tracked, still 200.
	other := sipParticipant("+97699000000", "+97600000000", nil)
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "participant_joined", Room: &livekit.Room{Name: "call-_zzz"}, Participant: other}).Code)
	_, err = e.db.GetCallByRoom(context.Background(), "call-_zzz")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestWebhookRoomStartedWithMetadata(t *testing.T) {
	e := newEnv(t)
	num := e.seedNumber(e.org.ID, "+97677001234", nil)
	room := "call-_meta"
	md := `{"sipNumberId":"` + num.ID.String() + `","direction":"inbound"}`
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "room_started", Room: &livekit.Room{Name: room, Metadata: md}}).Code)
	c, err := e.db.GetCallByRoom(context.Background(), room)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusRinging, c.Status)
	assert.Equal(t, "+97677001234", c.ToNumber)

	// Room closes before anyone answered → no_answer.
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "room_finished", Room: &livekit.Room{Name: room}}).Code)
	got := e.call(c.ID)
	assert.Equal(t, domain.StatusNoAnswer, got.Status)
	assert.Equal(t, "no_answer", got.EndReason)
}

func TestWebhookOutbound(t *testing.T) {
	e := newEnv(t)
	campID := uuid.New()
	c := e.seedCall(e.org.ID, domain.StatusRinging, func(c *domain.Call) {
		c.Direction, c.FromNumber, c.ToNumber, c.CampaignID = domain.DirectionOutbound, "", "", &campID
	})
	// Joined while still dialing: not answered.
	sip := sipParticipant("+97699112233", "+97677001234", map[string]string{attrSIPCallStatus: "dialing"})
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "participant_joined", Room: &livekit.Room{Name: c.RoomName}, Participant: sip}).Code)
	got := e.call(c.ID)
	assert.Equal(t, domain.StatusRinging, got.Status)
	assert.Equal(t, "+97699112233", got.ToNumber)
	assert.Equal(t, "+97677001234", got.FromNumber)

	// Callee rejected.
	sip.DisconnectReason = livekit.DisconnectReason_USER_REJECTED
	require.Equal(t, http.StatusOK, e.webhook(&livekit.WebhookEvent{Event: "participant_left", Room: &livekit.Room{Name: c.RoomName}, Participant: sip}).Code)
	got = e.call(c.ID)
	assert.Equal(t, domain.StatusBusy, got.Status)
	assert.Equal(t, []uuid.UUID{c.ID}, e.ctl.ended)
}
