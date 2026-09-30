package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type bootstrapBody struct {
	Call         domain.Call         `json:"call"`
	Org          domain.Organization `json:"org"`
	SIPNumber    *domain.SIPNumber   `json:"sipNumber"`
	Profile      domain.AgentProfile `json:"profile"`
	LLM          *llmWire            `json:"llm"`
	LLMFallbacks []llmWire           `json:"llmFallbacks"`
	Lexicon      []lexiconEntry      `json:"lexicon"`
	Contact      *domain.Contact     `json:"contact"`
	Campaign     *campaignInfo       `json:"campaign"`
}

type llmWire struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	APIKey string    `json:"apiKey"`
}

func TestAgentAuth(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/internal/agent/bootstrap?room=x", "/internal/agent/events", "/internal/agent/lexicon-hit"} {
		w := e.do(http.MethodGet, path, e.adminTok, nil) // a user JWT is not an agent token
		requireErr(t, w, http.StatusUnauthorized, "unauthorized")
	}
}

func TestAgentBootstrapInbound(t *testing.T) {
	e := newEnv(t)
	fb3 := e.seedLLM(e.org.ID, "fb3", "sk-fb3-000000000", false, nil)
	fb2 := e.seedLLM(e.org.ID, "fb2", "sk-fb2-000000000", false, &fb3.ID)
	fb1 := e.seedLLM(e.org.ID, "fb1", "sk-fb1-000000000", false, &fb2.ID)
	primary := e.seedLLM(e.org.ID, "primary", "sk-primary-00000", false, &fb1.ID)
	// A 4th hop must be cut off.
	fb4 := e.seedLLM(e.org.ID, "fb4", "sk-fb4-000000000", false, nil)
	fb3.FallbackID = &fb4.ID
	require.NoError(t, e.db.UpdateLLMConfig(context.Background(), &fb3))
	p := e.seedProfile(e.org.ID, &primary.ID)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	ct := e.seedContact(e.org.ID, "+97699112233", "Bat")
	require.NoError(t, e.db.AddCorrection(context.Background(), &domain.LexiconCorrection{OrgID: e.org.ID, Wrong: "калл", Correct: "Call", Scope: domain.ScopeSTT}))
	require.NoError(t, e.db.AddCorrection(context.Background(), &domain.LexiconCorrection{OrgID: e.org2.ID, Wrong: "other", Correct: "x", Scope: domain.ScopeSTT}))

	w := e.agent(http.MethodGet, "/internal/agent/bootstrap?room=call-_abc&sipNumber=%2B97677001234&from=%2B97699112233&to=%2B97677001234&direction=inbound", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	b := decode[bootstrapBody](t, w)
	assert.Equal(t, "call-_abc", b.Call.RoomName)
	assert.Equal(t, domain.DirectionInbound, b.Call.Direction)
	assert.Equal(t, e.org.ID, b.Org.ID)
	require.NotNil(t, b.SIPNumber)
	assert.Equal(t, num.ID, b.SIPNumber.ID)
	assert.Equal(t, p.ID, b.Profile.ID)
	require.NotNil(t, b.LLM)
	assert.Equal(t, "sk-primary-00000", b.LLM.APIKey)
	require.Len(t, b.LLMFallbacks, 3)
	assert.Equal(t, []string{"fb1", "fb2", "fb3"}, []string{b.LLMFallbacks[0].Name, b.LLMFallbacks[1].Name, b.LLMFallbacks[2].Name})
	assert.Equal(t, "sk-fb2-000000000", b.LLMFallbacks[1].APIKey)
	require.Len(t, b.Lexicon, 1)
	assert.Equal(t, "калл", b.Lexicon[0].Wrong)
	require.NotNil(t, b.Contact)
	assert.Equal(t, ct.ID, b.Contact.ID)
	assert.Nil(t, b.Campaign)
	assert.Len(t, e.bus.ofType(domain.EventCallStarted), 1)

	// Second bootstrap for the same room reuses the call.
	w = e.agent(http.MethodGet, "/internal/agent/bootstrap?room=call-_abc", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, b.Call.ID, decode[bootstrapBody](t, w).Call.ID)

	// Unknown number → 404; missing params → 400.
	requireErr(t, e.agent(http.MethodGet, "/internal/agent/bootstrap?room=call-_nope&sipNumber=%2B1555", nil), http.StatusNotFound, "not_found")
	requireErr(t, e.agent(http.MethodGet, "/internal/agent/bootstrap", nil), http.StatusBadRequest, "invalid")
}

func TestAgentBootstrapCampaign(t *testing.T) {
	e := newEnv(t)
	llm := e.seedLLM(e.org.ID, "default", "sk-default-0000", true, nil)
	p := e.seedProfile(e.org.ID, nil) // no LLM → org default
	num := e.seedNumber(e.org.ID, "+97677001234", nil)
	camp := &domain.Campaign{ID: uuid.New(), OrgID: e.org.ID, Name: "Debt", Script: "Say {debt}", AgentProfileID: &p.ID, SIPNumberID: &num.ID, Status: domain.CampaignRunning}
	callID := uuid.New()
	target := domain.CampaignTarget{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000001", Name: "Bat", Vars: map[string]string{"debt": "5000"}, CallID: &callID}
	require.NoError(t, e.db.CreateCampaign(context.Background(), camp, []domain.CampaignTarget{
		{ID: uuid.New(), CampaignID: camp.ID, Phone: "+97699000002"}, target,
	}))
	e.seedCall(e.org.ID, domain.StatusRinging, func(c *domain.Call) {
		c.ID, c.RoomName, c.Direction, c.CampaignID, c.SIPNumberID = callID, "call-"+callID.String(), domain.DirectionOutbound, &camp.ID, &num.ID
		c.ToNumber, c.FromNumber = "+97699000001", "+97677001234"
	})

	w := e.agent(http.MethodGet, "/internal/agent/bootstrap?room=call-"+callID.String()+"&callId="+callID.String()+"&direction=outbound", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	b := decode[bootstrapBody](t, w)
	assert.Equal(t, p.ID, b.Profile.ID, "profile from campaign")
	require.NotNil(t, b.LLM)
	assert.Equal(t, llm.ID, b.LLM.ID)
	assert.Equal(t, "sk-default-0000", b.LLM.APIKey)
	assert.Empty(t, b.LLMFallbacks)
	require.NotNil(t, b.Campaign)
	assert.Equal(t, "Say {debt}", b.Campaign.Script)
	assert.Equal(t, "5000", b.Campaign.Vars["debt"])
	assert.Equal(t, "Bat", b.Campaign.Vars["name"])
	assert.Equal(t, p.ID, *e.call(callID).AgentProfileID)

	requireErr(t, e.agent(http.MethodGet, "/internal/agent/bootstrap?callId="+uuid.NewString(), nil), http.StatusNotFound, "not_found")
}

func TestAgentEvents(t *testing.T) {
	e := newEnv(t)
	campID := uuid.New()
	c := e.seedCall(e.org.ID, domain.StatusRinging, func(c *domain.Call) { c.CampaignID = &campID })
	cid := c.ID.String()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	body := map[string]any{"events": []map[string]any{
		{"id": "ev1", "type": "call.answered", "callId": cid, "at": now, "payload": map[string]any{}},
		{"id": "ev2", "type": "transcript.partial", "callId": cid, "at": now, "payload": map[string]any{"speaker": "customer", "text": "сай", "startMs": 0}},
		{"id": "ev3", "type": "transcript.final", "callId": cid, "at": "", "payload": map[string]any{"turn": map[string]any{"id": "", "speaker": "customer", "text": "Сайн байна уу", "rawText": "сайн байна уу", "confidence": 0.9, "startMs": 0, "endMs": 900}}},
		{"id": "ev4", "type": "transcript.final", "callId": cid, "payload": map[string]any{"turn": map[string]any{"speaker": "agent", "text": "Сайн, туслах уу?"}}},
		{"id": "ev5", "type": "agent.state", "callId": cid, "payload": map[string]any{"state": "thinking", "llmModel": "openai/gpt-4.1-mini"}},
		{"id": "ev6", "type": "agent.state", "callId": uuid.NewString(), "payload": map[string]any{"state": "idle"}},
		{"id": "ev7", "type": "transcript.final", "callId": cid, "payload": map[string]any{"turn": map[string]any{"speaker": "robot", "text": "x"}}},
		{"id": "ev8", "type": "lexicon.updated", "callId": cid, "payload": map[string]any{}},
	}}
	w := e.agent(http.MethodPost, "/internal/agent/events", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"accepted":5}`, w.Body.String())

	got := e.call(c.ID)
	assert.Equal(t, domain.StatusActive, got.Status)
	require.NotNil(t, got.AnsweredAt)

	turns, _ := e.db.ListTurns(context.Background(), c.ID)
	require.Len(t, turns, 2)
	assert.Equal(t, 1, turns[0].Seq)
	assert.Equal(t, 2, turns[1].Seq)
	assert.NotEqual(t, uuid.Nil, turns[0].ID)
	assert.Equal(t, "сайн байна уу", turns[0].RawText)
	assert.True(t, turns[0].IsFinal)

	finals := e.bus.ofType(domain.EventTranscriptFinal)
	require.Len(t, finals, 2)
	assert.Equal(t, "ev3", finals[0].ID)
	assert.Equal(t, e.org.ID, finals[0].OrgID)
	pt := finals[0].Payload.(map[string]any)["turn"].(*domain.TranscriptTurn)
	assert.Equal(t, turns[0].ID, pt.ID)
	assert.Len(t, e.bus.ofType(domain.EventTranscriptPartial), 1)
	states := e.bus.ofType(domain.EventAgentState)
	require.Len(t, states, 1)
	assert.Equal(t, "ev5", states[0].ID)
	assert.Len(t, e.bus.ofType(domain.EventCallAnswered), 1)

	// Next batch continues the sequence, then ends the call.
	body = map[string]any{"events": []map[string]any{
		{"type": "transcript.final", "callId": cid, "payload": map[string]any{"turn": map[string]any{"speaker": "customer", "text": "Баярлалаа"}}},
		{"type": "call.ended", "callId": cid, "payload": map[string]any{"endReason": "hangup_customer", "summary": "Asked about balance", "sentiment": "positive", "intent": "balance_inquiry", "durationSec": 42, "llmModelUsed": "google/gemini-2.5-flash"}},
	}}
	w = e.agent(http.MethodPost, "/internal/agent/events", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"accepted":2}`, w.Body.String())
	turns, _ = e.db.ListTurns(context.Background(), c.ID)
	require.Len(t, turns, 3)
	assert.Equal(t, 3, turns[2].Seq)

	got = e.call(c.ID)
	assert.Equal(t, domain.StatusCompleted, got.Status)
	assert.Equal(t, "hangup_customer", got.EndReason)
	assert.Equal(t, "Asked about balance", got.Summary)
	assert.Equal(t, domain.SentimentPositive, got.Sentiment)
	assert.Equal(t, "balance_inquiry", got.Intent)
	assert.Equal(t, 42, got.DurationSec)
	assert.Equal(t, "google/gemini-2.5-flash", got.LLMModelUsed)
	ended := e.bus.ofType(domain.EventCallEnded)
	require.Len(t, ended, 1)
	assert.Equal(t, "Asked about balance", ended[0].Payload.(map[string]any)["summary"])
	assert.Equal(t, []uuid.UUID{c.ID}, e.ctl.ended)

	// A late call.ended (e.g. after the webhook finalized) only updates analysis.
	e.bus.reset()
	body = map[string]any{"events": []map[string]any{
		{"type": "call.ended", "callId": cid, "payload": map[string]any{"summary": "Updated summary"}},
	}}
	require.Equal(t, http.StatusOK, e.agent(http.MethodPost, "/internal/agent/events", body).Code)
	assert.Equal(t, "Updated summary", e.call(c.ID).Summary)
	assert.Empty(t, e.bus.ofType(domain.EventCallEnded))
	assert.Len(t, e.bus.ofType(domain.EventCallUpdated), 1)
	assert.Len(t, e.ctl.ended, 1)

	requireErr(t, e.agent(http.MethodPost, "/internal/agent/events", nil), http.StatusBadRequest, "invalid")
}

func TestAgentLexiconHit(t *testing.T) {
	e := newEnv(t)
	c := domain.LexiconCorrection{OrgID: e.org.ID, Wrong: "a", Correct: "b", Scope: domain.ScopeSTT}
	require.NoError(t, e.db.AddCorrection(context.Background(), &c))
	w := e.agent(http.MethodPost, "/internal/agent/lexicon-hit", map[string]any{"ids": []string{c.ID.String(), c.ID.String()}})
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	all, _ := e.db.ListCorrections(context.Background(), e.org.ID)
	assert.Equal(t, 2, all[0].HitCount)
	requireErr(t, e.agent(http.MethodPost, "/internal/agent/lexicon-hit", map[string]any{"ids": []string{"nope"}}), http.StatusBadRequest, "invalid")
}
