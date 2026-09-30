package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/routing"
)

const (
	maxFallbackDepth = 3
	maxEventsBody    = 4 << 20
	maxEventsPerPost = 500
)

// llmWithKey exposes the decrypted API key to the agent worker only.
type llmWithKey struct {
	domain.LLMConfig
	APIKey string `json:"apiKey"`
}

func withKey(c *domain.LLMConfig) *llmWithKey {
	if c == nil {
		return nil
	}
	return &llmWithKey{LLMConfig: *c, APIKey: c.APIKey}
}

type lexiconEntry struct {
	ID       uuid.UUID           `json:"id"`
	Wrong    string              `json:"wrong"`
	Correct  string              `json:"correct"`
	Scope    domain.LexiconScope `json:"scope"`
	Phonetic string              `json:"phonetic,omitempty"`
}

type campaignInfo struct {
	ID       uuid.UUID                `json:"id"`
	Name     string                   `json:"name"`
	Script   string                   `json:"script"`
	Vars     map[string]string        `json:"vars"`
	Outcomes []domain.CampaignOutcome `json:"outcomes"`
}

type bootstrapResponse struct {
	Call         *domain.Call         `json:"call"`
	Org          *domain.Organization `json:"org"`
	SIPNumber    *domain.SIPNumber    `json:"sipNumber"`
	Profile      *domain.AgentProfile `json:"profile"`
	LLM          *llmWithKey          `json:"llm"`
	LLMFallbacks []llmWithKey         `json:"llmFallbacks"`
	Lexicon      []lexiconEntry       `json:"lexicon"`
	Contact      *domain.Contact      `json:"contact"`
	Campaign     *campaignInfo        `json:"campaign"`
	Knowledge    *knowledgeInfo       `json:"knowledge"`
	// Route is the inbound routing decision (nil for outbound calls).
	Route        *domain.ResolvedRoute `json:"route"`
	Entitlements bootstrapEntitlements `json:"entitlements"`
	Handoff      bootstrapHandoff      `json:"handoff"`
}

type bootstrapEntitlements struct {
	CanStart bool   `json:"canStart"`
	Reason   string `json:"reason,omitempty"`
}

type bootstrapHandoff struct {
	Enabled bool `json:"enabled"`
}

func (s *server) agentBootstrap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx := r.Context()
	room := strings.TrimSpace(q.Get("room"))
	callID, err := parseOptUUID(q.Get("callId"), "callId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if room == "" && callID == nil {
		s.writeErr(w, r, errInvalid("room or callId is required"))
		return
	}
	from, _ := normalizePhone(q.Get("from"))
	to, _ := normalizePhone(q.Get("to"))
	sipNum := strings.TrimSpace(q.Get("sipNumber"))
	direction := domain.CallDirection(q.Get("direction"))
	// profileId overrides the profile (DTMF menu choice: the agent re-runs
	// bootstrap with the chosen option's profile).
	profileOverride, err := parseOptUUID(q.Get("profileId"), "profileId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	var call *domain.Call
	if callID != nil {
		call, err = s.d.Call.GetCall(ctx, *callID)
		if errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, errNotFound("call"))
			return
		}
		if err != nil {
			s.writeErr(w, r, fmt.Errorf("get call: %w", err))
			return
		}
	} else if call, err = s.callByRoom(ctx, room); err != nil {
		s.writeErr(w, r, err)
		return
	}

	var num *domain.SIPNumber
	if call == nil {
		if direction == domain.DirectionOutbound {
			s.writeErr(w, r, errNotFound("call"))
			return
		}
		for _, cand := range []string{sipNum, to} {
			if num, err = s.sipNumberByNumber(ctx, cand); err != nil {
				s.writeErr(w, r, err)
				return
			}
			if num != nil {
				break
			}
		}
		if num == nil {
			s.writeErr(w, r, errNotFound("SIP number"))
			return
		}
		if call, err = s.createInboundCall(ctx, room, num, from, firstNonEmpty(to, num.Number)); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	changed := false

	org, err := s.d.Org.GetOrg(ctx, call.OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("get org: %w", err))
		return
	}

	if num == nil && call.SIPNumberID != nil {
		n, err := s.d.SIPNumber.GetSIPNumber(ctx, *call.SIPNumberID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, fmt.Errorf("get sip number: %w", err))
			return
		}
		num = n
	}
	if num == nil {
		cand := sipNum
		if cand == "" {
			cand = map[domain.CallDirection]string{domain.DirectionInbound: call.ToNumber, domain.DirectionOutbound: call.FromNumber}[call.Direction]
		}
		if n, err := s.sipNumberByNumber(ctx, cand); err == nil && n != nil && n.OrgID == call.OrgID {
			num = n
			call.SIPNumberID, changed = &n.ID, true
		}
	}
	if call.FromNumber == "" && from != "" {
		call.FromNumber, changed = from, true
	}
	if call.ToNumber == "" && to != "" {
		call.ToNumber, changed = to, true
	}

	// Campaign (outbound) — script and the target's CSV variables.
	var camp *domain.Campaign
	var campInfo *campaignInfo
	var target *domain.CampaignTarget
	if call.CampaignID != nil {
		c, err := s.d.Campaign.GetCampaign(ctx, *call.CampaignID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, fmt.Errorf("get campaign: %w", err))
			return
		}
		if c != nil {
			camp = c
			if target, err = s.findTarget(ctx, c.ID, call); err != nil {
				s.writeErr(w, r, err)
				return
			}
			campInfo = &campaignInfo{ID: c.ID, Name: c.Name, Script: c.Script, Vars: map[string]string{},
				Outcomes: append([]domain.CampaignOutcome{}, c.Outcomes...)}
			if target != nil {
				for k, v := range target.Vars {
					campInfo.Vars[k] = v
				}
				if target.Name != "" {
					if _, ok := campInfo.Vars["name"]; !ok {
						campInfo.Vars["name"] = target.Name
					}
				}
				campInfo.Vars["phone"] = target.Phone
			}
		}
	}

	// Inbound routing (business hours / after-hours / DTMF menu) picks the
	// profile of an inbound call; an explicit profileId (menu choice) wins.
	var route *domain.ResolvedRoute
	if call.Direction == domain.DirectionInbound && num != nil && profileOverride == nil {
		rr, pid := routing.ForNumber(num, org, s.now())
		route = &rr
		if pid != nil {
			call.AgentProfileID, changed = pid, true
		}
	}
	if profileOverride != nil {
		p, err := s.d.AgentProfile.GetAgentProfile(ctx, *profileOverride)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, fmt.Errorf("get agent profile: %w", err))
			return
		}
		if p == nil || p.OrgID != call.OrgID {
			s.writeErr(w, r, errNotFound("agent profile"))
			return
		}
		if call.AgentProfileID == nil || *call.AgentProfileID != p.ID {
			call.AgentProfileID, changed = &p.ID, true
		}
		route = &domain.ResolvedRoute{Mode: "direct", AgentProfileID: &p.ID}
	}

	// Agent profile: call → SIP number → campaign → first profile of the org.
	profile, err := s.resolveProfile(ctx, call, num, camp)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if call.AgentProfileID == nil && profile.ID != uuid.Nil {
		call.AgentProfileID, changed = &profile.ID, true
	}
	if route != nil && route.AgentProfileID == nil && route.Mode != "menu" {
		route.AgentProfileID = &profile.ID
	}

	ent := bootstrapEntitlements{CanStart: true}
	if s.d.Entitlements != nil {
		ok, reason, err := s.d.Entitlements.CanStartCall(ctx, call.OrgID)
		if err != nil {
			s.log.Warn().Err(err).Str("org", call.OrgID.String()).Msg("bootstrap: entitlement check failed")
		} else {
			ent = bootstrapEntitlements{CanStart: ok, Reason: reason}
		}
	}
	handoff := bootstrapHandoff{Enabled: true}
	if s.d.Entitlements != nil {
		if ok, err := s.d.Entitlements.HasFeature(ctx, call.OrgID, "handoff"); err == nil {
			handoff.Enabled = ok
		}
	}

	if profile.KnowledgeMode == "" {
		profile.KnowledgeMode = domain.KnowledgeOff
	}
	knowledge := s.bootstrapKnowledge(ctx, call.OrgID, profile)

	llm, fallbacks, err := s.resolveLLM(ctx, call.OrgID, profile)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}

	corrections, err := s.d.Lexicon.ListCorrections(ctx, call.OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list corrections: %w", err))
		return
	}
	lex := make([]lexiconEntry, 0, len(corrections))
	for _, c := range corrections {
		lex = append(lex, lexiconEntry{ID: c.ID, Wrong: c.Wrong, Correct: c.Correct, Scope: c.Scope, Phonetic: c.Phonetic})
	}

	if call.ContactID == nil && target != nil && target.ContactID != nil {
		call.ContactID, changed = target.ContactID, true
	}
	if s.linkContact(ctx, call) {
		changed = true
	}
	var contact *domain.Contact
	if call.ContactID != nil {
		ct, err := s.d.Contact.GetContact(ctx, *call.ContactID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, fmt.Errorf("get contact: %w", err))
			return
		}
		if ct != nil && ct.OrgID == call.OrgID {
			contact = ct
		}
	}

	if changed {
		call.UpdatedAt = s.now()
		if err := s.d.Call.UpdateCall(ctx, call); err != nil {
			s.writeErr(w, r, fmt.Errorf("update call: %w", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, bootstrapResponse{
		Call: call, Org: org, SIPNumber: num, Profile: profile,
		LLM: withKey(llm), LLMFallbacks: fallbacks, Lexicon: lex, Contact: contact, Campaign: campInfo,
		Knowledge: knowledge, Route: route, Entitlements: ent, Handoff: handoff,
	})
}

// findTarget locates the campaign target of a call: via Metadata["targetId"]
// when the dialer set it, otherwise by scanning the targets for CallID.
func (s *server) findTarget(ctx context.Context, campaignID uuid.UUID, call *domain.Call) (*domain.CampaignTarget, error) {
	var wantID uuid.UUID
	if v, ok := call.Metadata["targetId"].(string); ok {
		wantID, _ = uuid.Parse(v)
	}
	const page = 500
	for offset := 0; offset < 100*page; offset += page {
		ts, total, err := s.d.Campaign.ListTargets(ctx, campaignID, page, offset)
		if err != nil {
			return nil, fmt.Errorf("list targets: %w", err)
		}
		for i := range ts {
			t := ts[i]
			if (wantID != uuid.Nil && t.ID == wantID) || (t.CallID != nil && *t.CallID == call.ID) {
				return &t, nil
			}
		}
		if len(ts) < page || offset+page >= total {
			break
		}
	}
	return nil, nil
}

func (s *server) resolveProfile(ctx context.Context, call *domain.Call, num *domain.SIPNumber, camp *domain.Campaign) (*domain.AgentProfile, error) {
	var candidates []*uuid.UUID
	candidates = append(candidates, call.AgentProfileID)
	if num != nil {
		candidates = append(candidates, num.AgentProfileID)
	}
	if camp != nil {
		candidates = append(candidates, camp.AgentProfileID)
	}
	for _, id := range candidates {
		if id == nil {
			continue
		}
		p, err := s.d.AgentProfile.GetAgentProfile(ctx, *id)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get agent profile: %w", err)
		}
		if p != nil && p.OrgID == call.OrgID {
			return p, nil
		}
	}
	all, err := s.d.AgentProfile.ListAgentProfiles(ctx, call.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	if len(all) > 0 {
		return &all[0], nil
	}
	// Nothing configured yet: a built-in persona keeps the line answered.
	return &domain.AgentProfile{
		OrgID:          call.OrgID,
		Name:           "Default agent",
		SystemPrompt:   "You are a polite, concise phone assistant. Answer in Mongolian unless the caller uses another language.",
		Greeting:       "Сайн байна уу! Танд юугаар туслах вэ?",
		Language:       defaultLanguage,
		MaxDurationSec: defaultMaxDurationSec,
		Tools:          []string{"end_call"},
		KnowledgeMode:  domain.KnowledgeOff,
	}, nil
}

// resolveLLM returns the profile's (or the org's default) config and its
// fallback chain, all with API keys decrypted.
func (s *server) resolveLLM(ctx context.Context, orgID uuid.UUID, p *domain.AgentProfile) (*domain.LLMConfig, []llmWithKey, error) {
	var primary *domain.LLMConfig
	if p.LLMConfigID != nil {
		c, err := s.d.LLMConfig.GetLLMConfig(ctx, *p.LLMConfigID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, nil, fmt.Errorf("get llm config: %w", err)
		}
		if c != nil && c.OrgID == orgID {
			primary = c
		}
	}
	if primary == nil {
		c, err := s.d.LLMConfig.GetDefaultLLMConfig(ctx, orgID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, nil, fmt.Errorf("get default llm config: %w", err)
		}
		if c != nil && c.OrgID == orgID {
			primary = c
		}
	}
	fallbacks := []llmWithKey{}
	if primary == nil {
		return nil, fallbacks, nil
	}
	seen := map[uuid.UUID]bool{primary.ID: true}
	next := primary.FallbackID
	for len(fallbacks) < maxFallbackDepth && next != nil && !seen[*next] {
		seen[*next] = true
		c, err := s.d.LLMConfig.GetLLMConfig(ctx, *next)
		if errors.Is(err, domain.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("get fallback llm config: %w", err)
		}
		if c == nil || c.OrgID != orgID {
			break
		}
		fallbacks = append(fallbacks, *withKey(c))
		next = c.FallbackID
	}
	return primary, fallbacks, nil
}

// ----------------------------------------------------------------------------
// Events ingest
// ----------------------------------------------------------------------------

// agentEvent is the wire form of an Event sent by the Python worker. IDs are
// strings because the agent may leave them empty.
type agentEvent struct {
	ID      string           `json:"id"`
	Type    domain.EventType `json:"type"`
	CallID  string           `json:"callId"`
	At      string           `json:"at"`
	Payload json.RawMessage  `json:"payload"`
}

type agentTurn struct {
	ID         string         `json:"id"`
	Seq        int            `json:"seq"`
	Speaker    domain.Speaker `json:"speaker"`
	Text       string         `json:"text"`
	RawText    string         `json:"rawText"`
	Confidence float32        `json:"confidence"`
	StartMs    int            `json:"startMs"`
	EndMs      int            `json:"endMs"`
}

type callEndedPayload struct {
	EndReason    string  `json:"endReason"`
	Summary      string  `json:"summary"`
	Sentiment    string  `json:"sentiment"`
	Intent       string  `json:"intent"`
	DurationSec  float64 `json:"durationSec"`
	LLMModelUsed string  `json:"llmModelUsed"`
	// Outcome is one of the campaign's outcome codes (or ""); pointers tell
	// "absent" from "empty".
	Outcome     *string `json:"outcome"`
	OutcomeNote *string `json:"outcomeNote"`
	// Usage is the worker's provider usage (metering).
	Usage *domain.CallUsage `json:"usage"`
	// Callbacks are callbacks the customer asked for during the call.
	Callbacks []callbackIntent `json:"callbacks"`
}

// callbackIntent is a callback requested during the call (docs/EVENTS.md).
type callbackIntent struct {
	DueAt time.Time `json:"dueAt"`
	Note  string    `json:"note"`
	Phone string    `json:"phone"`
}

// callUpdatedPayload is the agent's call.updated event (handoff state).
type callUpdatedPayload struct {
	Handoff    *domain.HandoffState `json:"handoff"`
	OperatorID *uuid.UUID           `json:"operatorId"`
}

// ingestState caches per-request lookups.
type ingestState struct {
	calls   map[uuid.UUID]*domain.Call
	nextSeq map[uuid.UUID]int
}

func (s *server) agentEvents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxEventsBody)
	var body struct {
		Events []agentEvent `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeErr(w, r, errInvalid("malformed JSON: %v", err))
		return
	}
	if len(body.Events) > maxEventsPerPost {
		s.writeErr(w, r, errInvalid("at most %d events per request", maxEventsPerPost))
		return
	}
	ctx := r.Context()
	st := &ingestState{calls: map[uuid.UUID]*domain.Call{}, nextSeq: map[uuid.UUID]int{}}
	accepted := 0
	for _, ev := range body.Events {
		ok, err := s.ingestEvent(ctx, st, ev)
		if err != nil {
			s.log.Error().Err(err).Str("type", string(ev.Type)).Str("callId", ev.CallID).Msg("ingest agent event")
			continue
		}
		if ok {
			accepted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

func (s *server) ingestCall(ctx context.Context, st *ingestState, id uuid.UUID) (*domain.Call, error) {
	if c, ok := st.calls[id]; ok {
		return c, nil
	}
	c, err := s.d.Call.GetCall(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		st.calls[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get call: %w", err)
	}
	st.calls[id] = c
	return c, nil
}

func (s *server) ingestEvent(ctx context.Context, st *ingestState, ev agentEvent) (bool, error) {
	callID, err := uuid.Parse(ev.CallID)
	if err != nil {
		return false, nil
	}
	call, err := s.ingestCall(ctx, st, callID)
	if err != nil || call == nil {
		return false, err
	}
	out := domain.Event{ID: ev.ID, Type: ev.Type, OrgID: call.OrgID, CallID: &call.ID}
	if at, err := time.Parse(time.RFC3339Nano, ev.At); err == nil {
		out.At = at.UTC()
	}

	switch ev.Type {
	case domain.EventTranscriptFinal:
		var p struct {
			Turn agentTurn `json:"turn"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return false, nil
		}
		turn, err := s.persistTurn(ctx, st, call, p.Turn)
		if err != nil || turn == nil {
			return false, err
		}
		out.Payload = map[string]any{"turn": turn}
		s.publishEvent(ctx, out)
		return true, nil

	case domain.EventCallAnswered:
		if !call.Status.IsTerminal() && call.Status != domain.StatusActive {
			now := s.now()
			call.Status = domain.StatusActive
			if call.AnsweredAt == nil {
				call.AnsweredAt = &now
			}
			call.UpdatedAt = now
			if err := s.d.Call.UpdateCall(ctx, call); err != nil {
				return false, fmt.Errorf("update call: %w", err)
			}
			snap := *call
			out.Payload = map[string]any{"call": &snap}
			s.publishEvent(ctx, out)
		}
		return true, nil

	case domain.EventCallEnded:
		var p callEndedPayload
		if len(ev.Payload) > 0 {
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return false, nil
			}
		}
		o := callOutcome{
			Status:       statusForEndReason(p.EndReason),
			EndReason:    p.EndReason,
			Summary:      strings.TrimSpace(p.Summary),
			Intent:       strings.TrimSpace(p.Intent),
			DurationSec:  int(p.DurationSec + 0.5),
			LLMModelUsed: p.LLMModelUsed,
		}
		switch sen := domain.Sentiment(p.Sentiment); sen {
		case domain.SentimentPositive, domain.SentimentNeutral, domain.SentimentNegative:
			o.Sentiment = sen
		}
		if p.Outcome != nil || p.OutcomeNote != nil {
			var code, note string
			if p.Outcome != nil {
				code = *p.Outcome
			}
			if p.OutcomeNote != nil {
				note = *p.OutcomeNote
			}
			o.HasOutcome = true
			o.Outcome, o.OutcomeNote = cleanOutcome(code, note)
		}
		if p.Usage != nil {
			call.Usage = p.Usage
		}
		// finalizeCall stores the outcome on the call before notifying the
		// campaign engine (OnCallEnded), which copies it onto the target.
		if _, err := s.finalizeCall(ctx, call, o); err != nil {
			return false, err
		}
		s.scheduleRequestedCallbacks(ctx, call, p.Callbacks)
		return true, nil

	case domain.EventCallUpdated:
		var p callUpdatedPayload
		if len(ev.Payload) > 0 {
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return false, nil
			}
		}
		if p.Handoff != nil {
			switch *p.Handoff {
			case domain.HandoffNone, domain.HandoffRequested, domain.HandoffActive, domain.HandoffEnded:
			default:
				return false, nil
			}
			call.Handoff = *p.Handoff
			if p.OperatorID != nil {
				call.OperatorID = p.OperatorID
			}
			if s.d.SetHandoff != nil {
				if err := s.d.SetHandoff(ctx, call.ID, call.Handoff, call.OperatorID); err != nil {
					return false, fmt.Errorf("set handoff: %w", err)
				}
			} else {
				call.UpdatedAt = s.now()
				if err := s.d.Call.UpdateCall(ctx, call); err != nil {
					return false, fmt.Errorf("update call: %w", err)
				}
			}
		}
		snap := *call
		out.Payload = map[string]any{"call": &snap, "handoff": call.Handoff, "operatorId": call.OperatorID}
		s.publishEvent(ctx, out)
		return true, nil

	case domain.EventAgentState, domain.EventTranscriptPartial:
		if len(ev.Payload) == 0 {
			out.Payload = map[string]any{}
		} else {
			out.Payload = ev.Payload
		}
		s.publishEvent(ctx, out)
		return true, nil

	case domain.EventCallStarted, domain.EventCallRinging:
		snap := *call
		out.Payload = map[string]any{"call": &snap}
		s.publishEvent(ctx, out)
		return true, nil
	}
	return false, nil
}

// statusForEndReason maps an agent end reason to a terminal status; "" lets
// finalizeCall derive it.
func statusForEndReason(reason string) domain.CallStatus {
	switch reason {
	case "no_answer":
		return domain.StatusNoAnswer
	case "busy":
		return domain.StatusBusy
	case "voicemail":
		return domain.StatusVoicemail
	case "failed":
		return domain.StatusFailed
	case "hangup_customer", "hangup_agent", "max_duration", "transferred":
		return domain.StatusCompleted
	}
	return ""
}

func (s *server) persistTurn(ctx context.Context, st *ingestState, call *domain.Call, in agentTurn) (*domain.TranscriptTurn, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, nil
	}
	switch in.Speaker {
	case domain.SpeakerCustomer, domain.SpeakerAgent, domain.SpeakerHuman:
	default:
		return nil, nil
	}
	id, err := uuid.Parse(in.ID)
	if err != nil {
		id = uuid.New()
	}
	seq, ok := st.nextSeq[call.ID]
	if !ok {
		turns, err := s.d.Call.ListTurns(ctx, call.ID)
		if err != nil {
			return nil, fmt.Errorf("list turns: %w", err)
		}
		for _, t := range turns {
			seq = max(seq, t.Seq)
		}
		seq++
	}
	if in.Seq > 0 && in.Seq >= seq {
		seq = in.Seq
	}
	st.nextSeq[call.ID] = seq + 1
	t := &domain.TranscriptTurn{
		ID: id, CallID: call.ID, Seq: seq, Speaker: in.Speaker, Text: text, RawText: in.RawText,
		Confidence: in.Confidence, StartMs: in.StartMs, EndMs: in.EndMs, IsFinal: true, CreatedAt: s.now(),
	}
	if err := s.d.Call.AddTurn(ctx, t); err != nil {
		return nil, fmt.Errorf("add turn: %w", err)
	}
	return t, nil
}

func (s *server) agentLexiconHit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ids := make([]uuid.UUID, 0, len(req.IDs))
	for _, v := range req.IDs {
		id, err := uuid.Parse(v)
		if err != nil {
			s.writeErr(w, r, errInvalid("invalid id %q", v))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		if err := s.d.Lexicon.IncrementHits(r.Context(), ids); err != nil {
			s.writeErr(w, r, fmt.Errorf("increment hits: %w", err))
			return
		}
	}
	noContent(w)
}

// scheduleRequestedCallbacks creates the callbacks the agent reported in
// call.ended. Failures are logged; they never fail the event.
func (s *server) scheduleRequestedCallbacks(ctx context.Context, call *domain.Call, intents []callbackIntent) {
	if s.d.CreateCallback == nil || len(intents) == 0 {
		return
	}
	for _, in := range intents {
		phone := in.Phone
		if phone == "" {
			phone = call.ToNumber
			if call.Direction == domain.DirectionInbound {
				phone = call.FromNumber
			}
		}
		if phone == "" || in.DueAt.IsZero() {
			continue
		}
		cb := &domain.CallbackRequest{
			OrgID:          call.OrgID,
			SourceCallID:   &call.ID,
			ContactID:      call.ContactID,
			Phone:          phone,
			Note:           strings.TrimSpace(in.Note),
			DueAt:          in.DueAt.UTC(),
			SIPNumberID:    call.SIPNumberID,
			AgentProfileID: call.AgentProfileID,
		}
		if err := s.d.CreateCallback(ctx, cb); err != nil {
			s.log.Warn().Err(err).Str("call", call.ID.String()).Msg("agent-requested callback not scheduled")
		}
	}
}
