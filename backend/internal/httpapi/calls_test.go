package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestListCalls(t *testing.T) {
	e := newEnv(t)
	active := e.seedCall(e.org.ID, domain.StatusActive)
	e.seedCall(e.org.ID, domain.StatusRinging, func(c *domain.Call) { c.Direction = domain.DirectionOutbound })
	e.seedCall(e.org.ID, domain.StatusCompleted, func(c *domain.Call) { c.Summary = "wants refund" })

	cases := []struct {
		query  string
		status int
		n      int
	}{
		{"", 200, 3},
		{"?status=active,ringing", 200, 2},
		{"?status=active&direction=inbound", 200, 1},
		{"?direction=outbound", 200, 1},
		{"?q=refund", 200, 1},
		{"?limit=1&offset=0", 200, 1},
		{"?from=2020-01-01&to=2099-01-01T00:00:00Z", 200, 3},
		{"?status=bogus", 400, 0},
		{"?direction=sideways", 400, 0},
		{"?limit=0", 400, 0},
		{"?campaignId=nope", 400, 0},
		{"?from=yesterday", 400, 0},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			w := e.do(http.MethodGet, "/api/calls"+tc.query, e.opTok, nil)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				l := decode[list[domain.Call]](t, w)
				assert.Len(t, l.Items, tc.n)
			}
		})
	}
	l := decode[list[domain.Call]](t, e.do(http.MethodGet, "/api/calls?limit=1", e.opTok, nil))
	assert.Equal(t, 3, l.Total)

	act := decode[list[domain.Call]](t, e.do(http.MethodGet, "/api/calls/active", e.opTok, nil))
	assert.Len(t, act.Items, 2)
	_ = active
}

func TestGetCall(t *testing.T) {
	e := newEnv(t)
	ct := e.seedContact(e.org.ID, "+97699110000", "Bat")
	c := e.seedCall(e.org.ID, domain.StatusActive, func(c *domain.Call) { c.ContactID = &ct.ID })
	for i, txt := range []string{"hello", "hi"} {
		tt := domain.TranscriptTurn{CallID: c.ID, Seq: i + 1, Speaker: domain.SpeakerAgent, Text: txt}
		require.NoError(t, e.db.AddTurn(context.Background(), &tt))
	}
	w := e.do(http.MethodGet, "/api/calls/"+c.ID.String(), e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	res := decode[struct {
		Call    domain.Call             `json:"call"`
		Turns   []domain.TranscriptTurn `json:"turns"`
		Contact *domain.Contact         `json:"contact"`
	}](t, w)
	assert.Equal(t, c.ID, res.Call.ID)
	assert.Len(t, res.Turns, 2)
	require.NotNil(t, res.Contact)
	assert.Equal(t, "Bat", res.Contact.Name)

	noContact := e.seedCall(e.org.ID, domain.StatusActive)
	w = e.do(http.MethodGet, "/api/calls/"+noContact.ID.String(), e.opTok, nil)
	assert.Contains(t, w.Body.String(), `"contact":null`)
	assert.Contains(t, w.Body.String(), `"turns":[]`)
}

func TestHangup(t *testing.T) {
	e := newEnv(t)
	c := e.seedCall(e.org.ID, domain.StatusActive)
	w := e.do(http.MethodPost, "/api/calls/"+c.ID.String()+"/hangup", e.opTok, nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, []string{c.RoomName}, e.tel.hangups)
	got := e.call(c.ID)
	assert.Equal(t, domain.StatusCompleted, got.Status)
	assert.Equal(t, "hangup_agent", got.EndReason)
	require.NotNil(t, got.EndedAt)
	assert.GreaterOrEqual(t, got.DurationSec, 29)
	ended := e.bus.ofType(domain.EventCallEnded)
	require.Len(t, ended, 1)
	assert.Equal(t, e.org.ID, ended[0].OrgID)

	// Already terminal: telephony still asked, no second call.ended.
	w = e.do(http.MethodPost, "/api/calls/"+c.ID.String()+"/hangup", e.opTok, nil)
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Len(t, e.bus.ofType(domain.EventCallEnded), 1)
}

func TestTransferAndRecording(t *testing.T) {
	e := newEnv(t)
	c := e.seedCall(e.org.ID, domain.StatusActive, func(c *domain.Call) { c.ParticipantID = "sip_1"; c.RecordingURL = "https://s3/rec.ogg" })
	done := e.seedCall(e.org.ID, domain.StatusCompleted)

	w := e.do(http.MethodPost, "/api/calls/"+c.ID.String()+"/transfer", e.opTok, map[string]string{"toNumber": "+976 9911-2233"})
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, []string{c.RoomName + "|sip_1|+97699112233"}, e.tel.transfers)
	requireErr(t, e.do(http.MethodPost, "/api/calls/"+c.ID.String()+"/transfer", e.opTok, map[string]string{"toNumber": "abc"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/calls/"+done.ID.String()+"/transfer", e.opTok, map[string]string{"toNumber": "+97699112233"}), http.StatusConflict, "conflict")

	w = e.do(http.MethodGet, "/api/calls/"+c.ID.String()+"/recording", e.opTok, nil)
	require.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "https://s3/rec.ogg", w.Header().Get("Location"))
	requireErr(t, e.do(http.MethodGet, "/api/calls/"+done.ID.String()+"/recording", e.opTok, nil), http.StatusNotFound, "not_found")
}

func TestDial(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	inOnly := e.seedNumber(e.org.ID, "+97677005555", &p.ID)
	inOnly.AllowOutbound = false
	require.NoError(t, e.db.UpdateSIPNumber(context.Background(), &inOnly))
	ct := e.seedContact(e.org.ID, "+97699112233", "Dorj")

	w := e.do(http.MethodPost, "/api/calls/dial", e.opTok, map[string]string{"toNumber": "+97699112233", "sipNumberId": num.ID.String()})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	res := decode[struct {
		Call domain.Call `json:"call"`
	}](t, w)
	e.srv.bg.Wait()

	assert.Equal(t, "call-"+res.Call.ID.String(), res.Call.RoomName)
	assert.Equal(t, domain.DirectionOutbound, res.Call.Direction)
	assert.Equal(t, domain.StatusRinging, res.Call.Status)
	assert.Equal(t, "+97677001234", res.Call.FromNumber)
	require.NotNil(t, res.Call.ContactID)
	assert.Equal(t, ct.ID, *res.Call.ContactID)
	require.Len(t, e.tel.dials, 1)
	d := e.tel.dials[0]
	assert.False(t, d.WaitUntilAnswered)
	assert.Equal(t, res.Call.RoomName, d.RoomName)
	assert.Equal(t, p.ID, d.AgentProfile.ID)
	assert.Equal(t, res.Call.ID.String(), d.Metadata["callId"])
	assert.Len(t, e.bus.ofType(domain.EventCallStarted), 1)
	assert.Equal(t, "sip_+97699112233", e.call(res.Call.ID).ParticipantID)

	for name, body := range map[string]map[string]string{
		"bad number":    {"toNumber": "x", "sipNumberId": num.ID.String()},
		"no sip number": {"toNumber": "+97699112233"},
		"unknown sip":   {"toNumber": "+97699112233", "sipNumberId": uuid.NewString()},
		"inbound only":  {"toNumber": "+97699112233", "sipNumberId": inOnly.ID.String()},
		"bad profile":   {"toNumber": "+97699112233", "sipNumberId": num.ID.String(), "agentProfileId": uuid.NewString()},
	} {
		t.Run(name, func(t *testing.T) {
			requireErr(t, e.do(http.MethodPost, "/api/calls/dial", e.opTok, body), http.StatusBadRequest, "invalid")
		})
	}
}

func TestDialFailure(t *testing.T) {
	e := newEnv(t)
	e.tel.dialErr = errors.New("trunk down")
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	w := e.do(http.MethodPost, "/api/calls/dial", e.opTok, map[string]string{"toNumber": "+97699112233", "sipNumberId": num.ID.String()})
	require.Equal(t, http.StatusCreated, w.Code)
	e.srv.bg.Wait()
	id := decode[struct {
		Call domain.Call `json:"call"`
	}](t, w).Call.ID
	got := e.call(id)
	assert.Equal(t, domain.StatusFailed, got.Status)
	assert.Equal(t, "failed", got.EndReason)
	assert.Len(t, e.bus.ofType(domain.EventCallEnded), 1)
}

func TestStatsAndWS(t *testing.T) {
	e := newEnv(t)
	e.seedCall(e.org.ID, domain.StatusActive)
	e.seedCall(e.org.ID, domain.StatusCompleted)
	e.seedCall(e.org2.ID, domain.StatusActive)

	st := decode[domain.CallStats](t, e.do(http.MethodGet, "/api/stats", e.opTok, nil))
	assert.Equal(t, 2, st.TotalCalls)
	assert.Equal(t, 1, st.ActiveCalls)

	daily := decode[list[domain.DailyCallCount]](t, e.do(http.MethodGet, "/api/stats/daily?days=7", e.opTok, nil))
	assert.Len(t, daily.Items, 7)
	requireErr(t, e.do(http.MethodGet, "/api/stats/daily?days=0", e.opTok, nil), http.StatusBadRequest, "invalid")

	// WebSocket auth via ?token=.
	w := e.do(http.MethodGet, "/api/ws?token="+e.opTok, "", nil)
	assert.Equal(t, http.StatusSwitchingProtocols, w.Code)
	assert.Equal(t, e.org.ID, e.hub.orgID)
	assert.Len(t, e.hub.initial, 1)
	requireErr(t, e.do(http.MethodGet, "/api/ws?token=bad", "", nil), http.StatusUnauthorized, "unauthorized")
}
