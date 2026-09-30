package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type turnPatchResponse struct {
	Turn       domain.TranscriptTurn     `json:"turn"`
	Correction *domain.LexiconCorrection `json:"correction"`
}

func TestPatchTurnCreatesAndDedupesLexicon(t *testing.T) {
	e := newEnv(t)
	c := e.seedCall(e.org.ID, domain.StatusCompleted)
	turn := domain.TranscriptTurn{ID: uuid.New(), CallID: c.ID, Seq: 1, Speaker: domain.SpeakerCustomer, Text: "калл гоу сайн уу"}
	require.NoError(t, e.db.AddTurn(context.Background(), &turn))
	path := "/api/turns/" + turn.ID.String()

	// Text only.
	w := e.do(http.MethodPatch, path, e.opTok, map[string]string{"text": "fixed text"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res := decode[turnPatchResponse](t, w)
	assert.Equal(t, "fixed text", res.Turn.Text)
	assert.Nil(t, res.Correction)
	assert.Contains(t, w.Body.String(), `"correction":null`)

	// Text + correction → created.
	w = e.do(http.MethodPatch, path, e.opTok, map[string]string{"text": "CallGo сайн уу", "wrong": "калл гоу", "correct": "CallGo"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res = decode[turnPatchResponse](t, w)
	require.NotNil(t, res.Correction)
	first := *res.Correction
	assert.Equal(t, domain.ScopeSTT, first.Scope)
	require.NotNil(t, first.SourceTurn)
	assert.Equal(t, turn.ID, *first.SourceTurn)
	require.NotNil(t, first.CreatedBy)
	assert.Equal(t, e.operator.ID, *first.CreatedBy)
	ev := e.bus.ofType(domain.EventLexiconUpdated)
	require.Len(t, ev, 1)
	assert.Equal(t, "created", ev[0].Payload.(map[string]any)["action"])
	assert.Equal(t, []uuid.UUID{e.org.ID}, e.lex.invalidated)

	// Same wrong, different case → the existing correction is updated.
	w = e.do(http.MethodPatch, path, e.opTok, map[string]string{"wrong": "КАЛЛ ГОУ", "correct": "CallGo.mn", "scope": "both", "phonetic": "kol go"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res = decode[turnPatchResponse](t, w)
	require.NotNil(t, res.Correction)
	assert.Equal(t, first.ID, res.Correction.ID)
	assert.Equal(t, "CallGo.mn", res.Correction.Correct)
	assert.Equal(t, domain.ScopeBoth, res.Correction.Scope)
	all, _ := e.db.ListCorrections(context.Background(), e.org.ID)
	assert.Len(t, all, 1)
	ev = e.bus.ofType(domain.EventLexiconUpdated)
	require.Len(t, ev, 2)
	assert.Equal(t, "updated", ev[1].Payload.(map[string]any)["action"])

	for name, body := range map[string]map[string]string{
		"wrong only":  {"wrong": "x"},
		"empty":       {},
		"empty text":  {"text": "  "},
		"bad scope":   {"wrong": "a", "correct": "b", "scope": "ear"},
		"same values": {"wrong": "a", "correct": "a"},
	} {
		t.Run(name, func(t *testing.T) {
			requireErr(t, e.do(http.MethodPatch, path, e.opTok, body), http.StatusBadRequest, "invalid")
		})
	}
	requireErr(t, e.do(http.MethodPatch, "/api/turns/"+uuid.NewString(), e.opTok, map[string]string{"text": "x"}), http.StatusNotFound, "not_found")
}

func TestLexiconCRUD(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodPost, "/api/lexicon", e.opTok, map[string]string{"wrong": "хаан банк", "correct": "Хаан Банк", "scope": "stt"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	c := decode[struct {
		Correction domain.LexiconCorrection `json:"correction"`
	}](t, w).Correction
	requireErr(t, e.do(http.MethodPost, "/api/lexicon", e.opTok, map[string]string{"wrong": "ХААН БАНК", "correct": "x"}), http.StatusConflict, "conflict")
	requireErr(t, e.do(http.MethodPost, "/api/lexicon", e.opTok, map[string]string{"wrong": "", "correct": "x"}), http.StatusBadRequest, "invalid")

	w = e.do(http.MethodPost, "/api/lexicon", e.opTok, map[string]string{"wrong": "голомт", "correct": "Голомт", "scope": "both"})
	require.Equal(t, http.StatusCreated, w.Code)
	other := decode[struct {
		Correction domain.LexiconCorrection `json:"correction"`
	}](t, w).Correction

	l := decode[list[domain.LexiconCorrection]](t, e.do(http.MethodGet, "/api/lexicon", e.opTok, nil))
	assert.Equal(t, 2, l.Total)

	w = e.do(http.MethodPut, "/api/lexicon/"+c.ID.String(), e.opTok, map[string]string{"wrong": "хаан банк", "correct": "Khan Bank", "scope": "tts", "phonetic": "khaan bank"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"correct":"Khan Bank"`)
	requireErr(t, e.do(http.MethodPut, "/api/lexicon/"+c.ID.String(), e.opTok, map[string]string{"wrong": "Голомт", "correct": "y"}), http.StatusConflict, "conflict")
	requireErr(t, e.do(http.MethodPut, "/api/lexicon/"+uuid.NewString(), e.opTok, map[string]string{"wrong": "q", "correct": "y"}), http.StatusNotFound, "not_found")

	// Apply via the engine.
	w = e.do(http.MethodPost, "/api/lexicon/apply", e.opTok, map[string]string{"text": "abc"})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"text":"ABC"`)

	require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/lexicon/"+c.ID.String(), e.opTok, nil).Code)
	requireErr(t, e.do(http.MethodDelete, "/api/lexicon/"+c.ID.String(), e.opTok, nil), http.StatusNotFound, "not_found")
	actions := []string{}
	for _, ev := range e.bus.ofType(domain.EventLexiconUpdated) {
		actions = append(actions, ev.Payload.(map[string]any)["action"].(string))
	}
	assert.Equal(t, []string{"created", "created", "updated", "deleted"}, actions)

	// Fallback apply without an engine: whole-word, case-insensitive.
	noEngine := newEnv(t, func(d *Deps, _ *Config) { d.LexiconEngine = nil })
	require.NoError(t, noEngine.db.AddCorrection(context.Background(), &domain.LexiconCorrection{OrgID: noEngine.org.ID, Wrong: "голомт", Correct: "Голомт", Scope: domain.ScopeSTT}))
	w = noEngine.do(http.MethodPost, "/api/lexicon/apply", noEngine.opTok, map[string]string{"text": "ГОЛОМТ банк, голомтын"})
	require.Equal(t, http.StatusOK, w.Code)
	res := decode[struct {
		Text string       `json:"text"`
		Hits []LexiconHit `json:"hits"`
	}](t, w)
	assert.Equal(t, "Голомт банк, голомтын", res.Text)
	assert.Len(t, res.Hits, 1)
	_ = other
}

func TestContacts(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodPost, "/api/contacts", e.opTok, map[string]any{"phone": "+976 9911 2233", "name": "Bold", "tags": []string{"vip", " "}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	ct := decode[struct {
		Contact domain.Contact `json:"contact"`
	}](t, w).Contact
	assert.Equal(t, "+97699112233", ct.Phone)
	assert.Equal(t, []string{"vip"}, ct.Tags)

	// Upsert on org+phone keeps the id.
	w = e.do(http.MethodPost, "/api/contacts", e.opTok, map[string]any{"phone": "+97699112233", "name": "Bold B."})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), ct.ID.String())
	requireErr(t, e.do(http.MethodPost, "/api/contacts", e.opTok, map[string]any{"phone": "nope"}), http.StatusBadRequest, "invalid")

	e.seedCall(e.org.ID, domain.StatusCompleted, func(c *domain.Call) { c.FromNumber = "+97699112233"; c.ContactID = &ct.ID })
	e.seedCall(e.org.ID, domain.StatusCompleted)
	w = e.do(http.MethodGet, "/api/contacts/"+ct.ID.String(), e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	res := decode[struct {
		Contact domain.Contact `json:"contact"`
		Calls   []domain.Call  `json:"calls"`
	}](t, w)
	assert.Len(t, res.Calls, 1)

	l := decode[list[domain.Contact]](t, e.do(http.MethodGet, "/api/contacts?q=Bold", e.opTok, nil))
	assert.Equal(t, 1, l.Total)

	// Import.
	csv := "phone,name\n+97688000001,A\n,missing\nbad-phone,B\n+97688000002,C\n"
	w = e.multipart("/api/contacts/import", e.opTok, nil, csv)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	imp := decode[importResult](t, w)
	assert.Equal(t, 2, imp.Imported)
	assert.Equal(t, 2, imp.Skipped)
	assert.Len(t, imp.Errors, 2)
	requireErr(t, e.multipart("/api/contacts/import", e.opTok, nil, ""), http.StatusBadRequest, "invalid")

	require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/contacts/"+ct.ID.String(), e.opTok, nil).Code)
	requireErr(t, e.do(http.MethodGet, "/api/contacts/"+ct.ID.String(), e.opTok, nil), http.StatusNotFound, "not_found")
}

func TestCampaigns(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	num := e.seedNumber(e.org.ID, "+97677001234", &p.ID)
	csv := "phone,name,debt\n+97699000001,Bat,1000\n+97699000002,Dorj,2000\n+97699000001,Dup,0\nxx,Bad,0\n"
	fields := map[string]string{"name": "Collections", "script": "Remind about {debt}", "sipNumberId": num.ID.String(), "concurrency": "5"}

	w := e.multipart("/api/campaigns", e.adminTok, fields, csv)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	res := decode[struct {
		Campaign domain.Campaign `json:"campaign"`
		Targets  importResult    `json:"targets"`
	}](t, w)
	assert.Equal(t, 2, res.Targets.Imported)
	assert.Equal(t, 2, res.Targets.Skipped)
	assert.Equal(t, domain.CampaignDraft, res.Campaign.Status)
	assert.Equal(t, 5, res.Campaign.Concurrency)
	assert.Equal(t, 2, res.Campaign.MaxAttempts)
	assert.Equal(t, 2, res.Campaign.Total)
	require.NotNil(t, res.Campaign.AgentProfileID)
	assert.Equal(t, p.ID, *res.Campaign.AgentProfileID)
	id := res.Campaign.ID.String()

	for name, mut := range map[string]func(map[string]string){
		"concurrency 0":  func(m map[string]string) { m["concurrency"] = "0" },
		"concurrency 51": func(m map[string]string) { m["concurrency"] = "51" },
		"no name":        func(m map[string]string) { m["name"] = "" },
		"no sip number":  func(m map[string]string) { delete(m, "sipNumberId") },
		"bad attempts":   func(m map[string]string) { m["maxAttempts"] = "x" },
	} {
		t.Run(name, func(t *testing.T) {
			f := map[string]string{}
			for k, v := range fields {
				f[k] = v
			}
			mut(f)
			requireErr(t, e.multipart("/api/campaigns", e.adminTok, f, csv), http.StatusBadRequest, "invalid")
		})
	}
	requireErr(t, e.multipart("/api/campaigns", e.adminTok, fields, ""), http.StatusBadRequest, "invalid")
	requireErr(t, e.multipart("/api/campaigns", e.adminTok, fields, "phone\nxx\n"), http.StatusBadRequest, "invalid")
	requireErr(t, e.multipart("/api/campaigns", e.opTok, fields, csv), http.StatusForbidden, "forbidden")

	w = e.do(http.MethodGet, "/api/campaigns/"+id+"?limit=1", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	got := decode[struct {
		Campaign domain.Campaign             `json:"campaign"`
		Targets  list[domain.CampaignTarget] `json:"targets"`
	}](t, w)
	assert.Equal(t, 2, got.Targets.Total)
	require.Len(t, got.Targets.Items, 1)
	assert.Equal(t, "1000", got.Targets.Items[0].Vars["debt"])
	assert.Equal(t, domain.TargetPending, got.Targets.Items[0].Status)

	assert.Len(t, decode[list[domain.Campaign]](t, e.do(http.MethodGet, "/api/campaigns", e.opTok, nil)).Items, 1)
	assert.Empty(t, decode[list[domain.Campaign]](t, e.do(http.MethodGet, "/api/campaigns", e.othTok, nil)).Items)

	requireErr(t, e.do(http.MethodPost, "/api/campaigns/"+id+"/pause", e.adminTok, nil), http.StatusConflict, "conflict")
	w = e.do(http.MethodPost, "/api/campaigns/"+id+"/start", e.adminTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"running"`)
	requireErr(t, e.do(http.MethodDelete, "/api/campaigns/"+id, e.adminTok, nil), http.StatusConflict, "conflict")
	w = e.do(http.MethodPost, "/api/campaigns/"+id+"/pause", e.adminTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"paused"`)
	require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/campaigns/"+id, e.adminTok, nil).Code)
	requireErr(t, e.do(http.MethodGet, "/api/campaigns/"+id, e.adminTok, nil), http.StatusNotFound, "not_found")
}

func TestSIPNumbers(t *testing.T) {
	e := newEnv(t)
	p := e.seedProfile(e.org.ID, nil)
	body := map[string]any{"number": "+97677001234", "label": "Main", "agentProfileId": p.ID.String(), "allowInbound": true, "allowOutbound": true}
	w := e.do(http.MethodPost, "/api/sip-numbers", e.adminTok, body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	n := decode[struct {
		SIPNumber domain.SIPNumber `json:"sipNumber"`
	}](t, w).SIPNumber
	assert.True(t, n.Active)
	assert.Equal(t, "ST_in", n.InboundTrunkID)
	stored, _ := e.db.GetSIPNumber(context.Background(), n.ID)
	assert.Equal(t, "SDR_1", stored.DispatchRuleID)

	requireErr(t, e.do(http.MethodPost, "/api/sip-numbers", e.adminTok, body), http.StatusConflict, "conflict")
	requireErr(t, e.do(http.MethodPost, "/api/sip-numbers", e.adminTok, map[string]any{"number": "abc"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/sip-numbers", e.adminTok, map[string]any{"number": "+97677001299", "agentProfileId": uuid.NewString()}), http.StatusBadRequest, "invalid")

	// Deactivate → deprovision.
	body["active"] = false
	body["label"] = "Renamed"
	w = e.do(http.MethodPut, "/api/sip-numbers/"+n.ID.String(), e.adminTok, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	upd := decode[struct {
		SIPNumber domain.SIPNumber `json:"sipNumber"`
	}](t, w).SIPNumber
	assert.False(t, upd.Active)
	assert.Equal(t, "Renamed", upd.Label)
	assert.Empty(t, upd.InboundTrunkID)
	assert.Equal(t, []string{"+97677001234"}, e.tel.deprovisions)

	w = e.do(http.MethodPost, "/api/sip-numbers/"+n.ID.String()+"/provision", e.adminTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"inboundTrunkId":"ST_in"`)

	require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/sip-numbers/"+n.ID.String(), e.adminTok, nil).Code)
	assert.Len(t, e.tel.deprovisions, 2)
	assert.Empty(t, decode[list[domain.SIPNumber]](t, e.do(http.MethodGet, "/api/sip-numbers", e.adminTok, nil)).Items)

	// Provisioning failure rolls back the row.
	e.tel.provisionErr = errors.New("livekit unreachable")
	requireErr(t, e.do(http.MethodPost, "/api/sip-numbers", e.adminTok, map[string]any{"number": "+97677001111"}), http.StatusInternalServerError, "internal")
	_, err := e.db.GetSIPNumberByNumber(context.Background(), "+97677001111")
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAgentProfiles(t *testing.T) {
	e := newEnv(t)
	llm := e.seedLLM(e.org.ID, "main", "sk-live-1234567890", true, nil)
	foreignLLM := e.seedLLM(e.org2.ID, "theirs", "sk-live-1234567890", true, nil)
	body := map[string]any{"name": "Support", "systemPrompt": "Be nice", "greeting": "Сайн байна уу", "llmConfigId": llm.ID.String(),
		"sttProvider": "faster_whisper", "ttsProvider": "piper", "tools": []string{"end_call", "transfer_call"}, "transferNumber": "+97677000000"}
	w := e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	p := decode[struct {
		Profile domain.AgentProfile `json:"profile"`
	}](t, w).Profile
	assert.Equal(t, "mn", p.Language)
	assert.Equal(t, defaultMaxDurationSec, p.MaxDurationSec)

	for name, mut := range map[string]func(map[string]any){
		"no name":      func(m map[string]any) { m["name"] = "" },
		"bad tool":     func(m map[string]any) { m["tools"] = []string{"launch_missiles"} },
		"foreign llm":  func(m map[string]any) { m["llmConfigId"] = foreignLLM.ID.String() },
		"bad duration": func(m map[string]any) { m["maxDurationSec"] = 5 },
	} {
		t.Run(name, func(t *testing.T) {
			b := map[string]any{}
			for k, v := range body {
				b[k] = v
			}
			mut(b)
			requireErr(t, e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, b), http.StatusBadRequest, "invalid")
		})
	}

	body["language"] = "en"
	body["name"] = "Support EN"
	w = e.do(http.MethodPut, "/api/agent-profiles/"+p.ID.String(), e.adminTok, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"language":"en"`)
	assert.Len(t, decode[list[domain.AgentProfile]](t, e.do(http.MethodGet, "/api/agent-profiles", e.opTok, nil)).Items, 1)
	require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/agent-profiles/"+p.ID.String(), e.adminTok, nil).Code)
	requireErr(t, e.do(http.MethodGet, "/api/agent-profiles/"+p.ID.String(), e.adminTok, nil), http.StatusNotFound, "not_found")
}

func TestLLMConfigs(t *testing.T) {
	e := newEnv(t)
	const key = "sk-proj-SECRETSECRET-q9Zt"
	requireErr(t, e.do(http.MethodPost, "/api/llm-configs", e.adminTok, map[string]any{"name": "a", "provider": "openai", "model": "gpt-4.1"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/llm-configs", e.adminTok, map[string]any{"name": "a", "provider": "skynet", "model": "x", "apiKey": "k"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/llm-configs", e.adminTok, map[string]any{"name": "a", "provider": "ollama", "model": "llama3.1"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/llm-configs", e.adminTok, map[string]any{"name": "a", "provider": "openai", "model": "gpt-4.1", "apiKey": key, "temperature": 3}), http.StatusBadRequest, "invalid")

	w := e.do(http.MethodPost, "/api/llm-configs", e.adminTok, map[string]any{"name": "Primary", "provider": "openai", "model": "gpt-4.1-mini", "apiKey": key, "temperature": 0.2, "maxTokens": 300})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "SECRETSECRET")
	first := decode[struct {
		Config domain.LLMConfig `json:"config"`
	}](t, w).Config
	assert.True(t, first.IsDefault, "first config becomes default")
	assert.Equal(t, "sk-...q9Zt", first.APIKeyHint)
	assert.InDelta(t, 0.2, first.Temperature, 0.001)

	w = e.do(http.MethodPost, "/api/llm-configs", e.adminTok, map[string]any{"name": "Local", "provider": "ollama", "model": "qwen2.5", "baseUrl": "http://ollama:11434/", "isDefault": true, "fallbackId": first.ID.String()})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	second := decode[struct {
		Config domain.LLMConfig `json:"config"`
	}](t, w).Config
	assert.Equal(t, "http://ollama:11434", second.BaseURL)
	stored, _ := e.db.GetLLMConfig(context.Background(), first.ID)
	assert.False(t, stored.IsDefault, "isDefault is exclusive")
	assert.Equal(t, key, stored.APIKey, "clearing default keeps the key")

	// Cycle: first → second → first.
	requireErr(t, e.do(http.MethodPut, "/api/llm-configs/"+first.ID.String(), e.adminTok, map[string]any{"name": "Primary", "provider": "openai", "model": "gpt-4.1", "fallbackId": second.ID.String()}), http.StatusBadRequest, "invalid")

	// PUT without apiKey keeps the stored key.
	w = e.do(http.MethodPut, "/api/llm-configs/"+first.ID.String(), e.adminTok, map[string]any{"name": "Primary v2", "provider": "openai", "model": "gpt-4.1", "isDefault": true})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	stored, _ = e.db.GetLLMConfig(context.Background(), first.ID)
	assert.Equal(t, key, stored.APIKey)
	assert.Equal(t, "Primary v2", stored.Name)
	assert.True(t, stored.IsDefault)
	sec, _ := e.db.GetLLMConfig(context.Background(), second.ID)
	assert.False(t, sec.IsDefault)

	w = e.do(http.MethodGet, "/api/llm-configs", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "SECRETSECRET")
	assert.NotContains(t, w.Body.String(), `"apiKey"`)
	assert.Len(t, decode[list[domain.LLMConfig]](t, w).Items, 2)

	// Catalog.
	w = e.do(http.MethodGet, "/api/llm-configs/catalog", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code)
	cat := decode[struct {
		Providers []catalogEntry `json:"providers"`
	}](t, w)
	require.Len(t, cat.Providers, 6)
	assert.Equal(t, []string{"claude-sonnet-4-5", "claude-haiku-4-5"}, cat.Providers[1].Models)
	assert.True(t, cat.Providers[4].NeedsBaseURL)
	assert.Contains(t, w.Body.String(), `"needsApiKey"`)

	// Test endpoint receives the decrypted key.
	w = e.do(http.MethodPost, "/api/llm-configs/"+first.ID.String()+"/test", e.adminTok, map[string]string{"prompt": "ping"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	tr := decode[llmTestResponse](t, w)
	assert.True(t, tr.OK)
	assert.Equal(t, "pong: ping", tr.Reply)
	assert.Equal(t, int64(12), tr.LatencyMs)
	assert.Equal(t, key, e.tester.last.APIKey)
	e.tester.err = errors.New("401 invalid key")
	tr = decode[llmTestResponse](t, e.do(http.MethodPost, "/api/llm-configs/"+first.ID.String()+"/test", e.adminTok, nil))
	assert.False(t, tr.OK)
	assert.Equal(t, "401 invalid key", tr.Error)

	noTester := newEnv(t, func(d *Deps, _ *Config) { d.LLMTester = nil })
	c := noTester.seedLLM(noTester.org.ID, "x", "sk-1234567890", true, nil)
	tr = decode[llmTestResponse](t, noTester.do(http.MethodPost, "/api/llm-configs/"+c.ID.String()+"/test", noTester.adminTok, nil))
	assert.False(t, tr.OK)
	assert.True(t, strings.Contains(tr.Error, "not configured"))

	// Delete unlinks fallbacks.
	require.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/llm-configs/"+first.ID.String(), e.adminTok, nil).Code)
	sec, _ = e.db.GetLLMConfig(context.Background(), second.ID)
	assert.Nil(t, sec.FallbackID)
}
