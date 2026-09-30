package httpapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const dialTimeout = 90 * time.Second

// loadCall fetches a call and hides calls of other organisations.
func (s *server) loadCall(ctx context.Context, orgID, id uuid.UUID) (*domain.Call, error) {
	c, err := s.d.Call.GetCall(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != orgID)) {
		return nil, errNotFound("call")
	}
	if err != nil {
		return nil, fmt.Errorf("get call: %w", err)
	}
	return c, nil
}

func parseTimeParam(v, field string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return &t, nil
	}
	return nil, errInvalid("%s must be RFC-3339 or YYYY-MM-DD", field)
}

var validStatuses = map[domain.CallStatus]bool{
	domain.StatusQueued: true, domain.StatusRinging: true, domain.StatusActive: true,
	domain.StatusCompleted: true, domain.StatusFailed: true, domain.StatusNoAnswer: true,
	domain.StatusBusy: true, domain.StatusVoicemail: true,
}

func (s *server) listCalls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.CallFilter{OrgID: claimsOf(r).OrgID, Search: strings.TrimSpace(q.Get("q"))}
	var err error
	if f.Limit, err = queryInt(r, "limit", 50, 1, 500); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if f.Offset, err = queryInt(r, "offset", 0, 0, 1<<30); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if st := q.Get("status"); st != "" {
		for _, p := range strings.Split(st, ",") {
			cs := domain.CallStatus(strings.TrimSpace(p))
			if cs == "" {
				continue
			}
			if !validStatuses[cs] {
				s.writeErr(w, r, errInvalid("unknown status %q", cs))
				return
			}
			f.Status = append(f.Status, cs)
		}
	}
	switch d := domain.CallDirection(q.Get("direction")); d {
	case "", domain.DirectionInbound, domain.DirectionOutbound:
		f.Direction = d
	default:
		s.writeErr(w, r, errInvalid("direction must be inbound or outbound"))
		return
	}
	if f.CampaignID, err = parseOptUUID(q.Get("campaignId"), "campaignId"); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if f.From, err = parseTimeParam(q.Get("from"), "from"); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if f.To, err = parseTimeParam(q.Get("to"), "to"); err != nil {
		s.writeErr(w, r, err)
		return
	}
	calls, total, err := s.d.Call.ListCalls(r.Context(), f)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list calls: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(calls, total))
}

func (s *server) activeCalls(w http.ResponseWriter, r *http.Request) {
	calls, err := s.d.Call.ListActiveCalls(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list active calls: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(calls, len(calls)))
}

func (s *server) getCall(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.loadCall(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	turns, err := s.d.Call.ListTurns(ctx, c.ID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list turns: %w", err))
		return
	}
	if turns == nil {
		turns = []domain.TranscriptTurn{}
	}
	var contact *domain.Contact
	if c.ContactID != nil {
		ct, err := s.d.Contact.GetContact(ctx, *c.ContactID)
		switch {
		case err == nil && ct != nil && ct.OrgID == c.OrgID:
			contact = ct
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			s.writeErr(w, r, fmt.Errorf("get contact: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": c, "turns": turns, "contact": contact})
}

func (s *server) hangup(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.loadCall(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if c.RoomName != "" {
		if err := s.d.Telephony.Hangup(ctx, c.RoomName); err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, fmt.Errorf("hangup room %s: %w", c.RoomName, err))
			return
		}
	}
	if !c.Status.IsTerminal() {
		if _, err := s.finalizeCall(ctx, c, callOutcome{Status: domain.StatusCompleted, EndReason: "hangup_agent"}); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	noContent(w)
}

func (s *server) transfer(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req struct {
		ToNumber string `json:"toNumber"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	to, err := requirePhone(req.ToNumber, "toNumber")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.loadCall(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if c.Status != domain.StatusActive {
		s.writeErr(w, r, errConflict("call is not active"))
		return
	}
	if err := s.d.Telephony.TransferCall(ctx, c.RoomName, c.ParticipantID, to); err != nil {
		s.writeErr(w, r, fmt.Errorf("transfer call: %w", err))
		return
	}
	noContent(w)
}

func (s *server) recording(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	c, err := s.loadCall(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if c.RecordingURL == "" {
		s.writeErr(w, r, errNotFound("recording"))
		return
	}
	http.Redirect(w, r, c.RecordingURL, http.StatusFound)
}

func (s *server) dial(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ToNumber       string `json:"toNumber"`
		SIPNumberID    string `json:"sipNumberId"`
		AgentProfileID string `json:"agentProfileId"`
		ContactID      string `json:"contactId"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	to, err := requirePhone(req.ToNumber, "toNumber")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.checkDialable(ctx, orgID, to); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.checkCanStartCall(ctx, orgID); err != nil {
		s.writeErr(w, r, err)
		return
	}
	numID, err := parseOptUUID(req.SIPNumberID, "sipNumberId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if numID == nil {
		s.writeErr(w, r, errInvalid("sipNumberId is required"))
		return
	}
	num, err := s.loadSIPNumber(ctx, orgID, *numID)
	if err != nil {
		s.writeErr(w, r, asInvalidRef(err, "sipNumberId"))
		return
	}
	if !num.Active || !num.AllowOutbound {
		s.writeErr(w, r, errInvalid("SIP number %s is not enabled for outbound calls", num.Number))
		return
	}
	profID, err := parseOptUUID(req.AgentProfileID, "agentProfileId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if profID == nil {
		profID = num.AgentProfileID
	}
	if profID == nil {
		s.writeErr(w, r, errInvalid("agentProfileId is required (the SIP number has no default profile)"))
		return
	}
	profile, err := s.loadProfile(ctx, orgID, *profID)
	if err != nil {
		s.writeErr(w, r, asInvalidRef(err, "agentProfileId"))
		return
	}
	contactID, err := parseOptUUID(req.ContactID, "contactId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if contactID != nil {
		ct, err := s.d.Contact.GetContact(ctx, *contactID)
		if err != nil || ct == nil || ct.OrgID != orgID {
			s.writeErr(w, r, errInvalid("contactId does not exist"))
			return
		}
	} else if ct, err := s.d.Contact.GetContactByPhone(ctx, orgID, to); err == nil && ct != nil {
		contactID = &ct.ID
	}

	now := s.now()
	id := uuid.New()
	call := &domain.Call{
		ID:             id,
		OrgID:          orgID,
		ContactID:      contactID,
		SIPNumberID:    &num.ID,
		AgentProfileID: &profile.ID,
		Direction:      domain.DirectionOutbound,
		Status:         domain.StatusRinging,
		FromNumber:     num.Number,
		ToNumber:       to,
		RoomName:       "call-" + id.String(),
		StartedAt:      now,
		Metadata:       map[string]any{"manual": true, "userId": claimsOf(r).UserID.String()},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.d.Call.CreateCall(ctx, call); err != nil {
		s.writeErr(w, r, fmt.Errorf("create call: %w", err))
		return
	}
	s.publishCall(ctx, domain.EventCallStarted, call)

	dialReq := domain.OutboundCallRequest{
		CallID:       call.ID,
		RoomName:     call.RoomName,
		FromNumber:   *num,
		ToNumber:     to,
		AgentProfile: *profile,
		Metadata: map[string]any{
			"callId":         call.ID.String(),
			"direction":      string(domain.DirectionOutbound),
			"sipNumberId":    num.ID.String(),
			"agentProfileId": profile.ID.String(),
		},
		WaitUntilAnswered: false,
		RingTimeout:       45 * time.Second,
	}
	if contactID != nil {
		dialReq.Metadata["contactId"] = contactID.String()
	}
	callID := call.ID
	s.goBackground(ctx, dialTimeout, func(ctx context.Context) { s.runDial(ctx, callID, dialReq) })
	writeJSON(w, http.StatusCreated, map[string]any{"call": call})
}

func (s *server) runDial(ctx context.Context, callID uuid.UUID, req domain.OutboundCallRequest) {
	res, dialErr := s.d.Telephony.Dial(ctx, req)
	c, err := s.d.Call.GetCall(ctx, callID)
	if err != nil {
		s.log.Error().Err(err).Str("callId", callID.String()).Msg("dial: reload call")
		return
	}
	if dialErr != nil || res.Error != "" {
		msg := res.Error
		if dialErr != nil {
			msg = dialErr.Error()
		}
		s.log.Warn().Str("callId", callID.String()).Str("error", msg).Msg("outbound dial failed")
		if c.Status.IsTerminal() {
			return
		}
		md := maps.Clone(c.Metadata) // never mutate a map that may be shared
		if md == nil {
			md = map[string]any{}
		}
		md["dialError"] = msg
		c.Metadata = md
		if _, err := s.finalizeCall(ctx, c, callOutcome{Status: domain.StatusFailed, EndReason: "failed"}); err != nil {
			s.log.Error().Err(err).Str("callId", callID.String()).Msg("dial: finalize failed call")
		}
		return
	}
	changed := false
	if res.ParticipantID != "" && c.ParticipantID == "" {
		c.ParticipantID, changed = res.ParticipantID, true
	}
	if res.SIPCallID != "" && c.SIPCallID == "" {
		c.SIPCallID, changed = res.SIPCallID, true
	}
	if res.Answered && !c.Status.IsTerminal() && c.Status != domain.StatusActive {
		now := s.now()
		c.Status, c.AnsweredAt, changed = domain.StatusActive, &now, true
	}
	if !changed {
		return
	}
	c.UpdatedAt = s.now()
	if err := s.d.Call.UpdateCall(ctx, c); err != nil {
		s.log.Error().Err(err).Str("callId", callID.String()).Msg("dial: update call")
		return
	}
	typ := domain.EventCallUpdated
	if c.Status == domain.StatusActive && res.Answered {
		typ = domain.EventCallAnswered
	}
	s.publishCall(ctx, typ, c)
}

// callOutcome describes how a call ended.
type callOutcome struct {
	Status       domain.CallStatus // empty = derive from state
	EndReason    string
	Summary      string
	Sentiment    domain.Sentiment
	Intent       string
	DurationSec  int
	LLMModelUsed string
	// Outcome / OutcomeNote are the campaign result chosen by the AI; set
	// only when HasOutcome.
	HasOutcome  bool
	Outcome     string
	OutcomeNote string
}

// finalizeCall moves a non-terminal call to a terminal state, persists it,
// publishes call.ended and notifies the campaign controller. For calls that
// are already terminal it only merges the analysis fields and publishes
// call.updated. It reports whether the call transitioned.
func (s *server) finalizeCall(ctx context.Context, c *domain.Call, o callOutcome) (bool, error) {
	now := s.now()
	transitioned := !c.Status.IsTerminal()
	if transitioned {
		st := o.Status
		if st == "" {
			switch {
			case c.Status == domain.StatusActive || c.AnsweredAt != nil:
				st = domain.StatusCompleted
			case c.Status == domain.StatusRinging:
				st = domain.StatusNoAnswer
			default:
				st = domain.StatusFailed
			}
		}
		c.Status = st
		c.EndedAt = &now
		if o.EndReason != "" {
			c.EndReason = o.EndReason
		}
		if c.EndReason == "" {
			switch st {
			case domain.StatusNoAnswer:
				c.EndReason = "no_answer"
			case domain.StatusFailed:
				c.EndReason = "failed"
			case domain.StatusBusy:
				c.EndReason = "busy"
			case domain.StatusVoicemail:
				c.EndReason = "voicemail"
			}
		}
		switch {
		case o.DurationSec > 0:
			c.DurationSec = o.DurationSec
		case c.AnsweredAt != nil:
			c.DurationSec = max(0, int(now.Sub(*c.AnsweredAt).Round(time.Second)/time.Second))
		}
	} else {
		if o.EndReason != "" && c.EndReason == "" {
			c.EndReason = o.EndReason
		}
		if o.DurationSec > 0 {
			c.DurationSec = o.DurationSec
		}
	}
	if o.Summary != "" {
		c.Summary = o.Summary
	}
	if o.Sentiment != "" {
		c.Sentiment = o.Sentiment
	}
	if o.Intent != "" {
		c.Intent = o.Intent
	}
	if o.LLMModelUsed != "" {
		c.LLMModelUsed = o.LLMModelUsed
	}
	if o.HasOutcome {
		c.Outcome, c.OutcomeNote = o.Outcome, o.OutcomeNote
	}
	c.UpdatedAt = now
	if err := s.d.Call.UpdateCall(ctx, c); err != nil {
		return false, fmt.Errorf("update call: %w", err)
	}
	if !transitioned {
		s.publishCall(ctx, domain.EventCallUpdated, c)
		return false, nil
	}
	id := c.ID
	snap := *c
	payload := map[string]any{"call": &snap, "endReason": c.EndReason, "durationSec": c.DurationSec}
	if c.Summary != "" {
		payload["summary"] = c.Summary
	}
	if c.Sentiment != "" {
		payload["sentiment"] = c.Sentiment
	}
	if c.Intent != "" {
		payload["intent"] = c.Intent
	}
	if c.LLMModelUsed != "" {
		payload["llmModelUsed"] = c.LLMModelUsed
	}
	if c.Outcome != "" || c.OutcomeNote != "" {
		payload["outcome"], payload["outcomeNote"] = c.Outcome, c.OutcomeNote
	}
	s.publish(ctx, c.OrgID, &id, domain.EventCallEnded, payload)
	if s.d.Campaigns != nil && c.CampaignID != nil {
		s.d.Campaigns.OnCallEnded(ctx, c)
	}
	s.runCallEndedHooks(ctx, c)
	return true, nil
}

// runCallEndedHooks runs Deps.CallEndedHooks (metering, post-call actions,
// callbacks) in the background on a snapshot of the call.
func (s *server) runCallEndedHooks(ctx context.Context, c *domain.Call) {
	if len(s.d.CallEndedHooks) == 0 {
		return
	}
	snap := *c
	hooks := s.d.CallEndedHooks
	s.goBackground(ctx, 2*time.Minute, func(ctx context.Context) {
		for _, h := range hooks {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						s.log.Error().Interface("panic", rec).Str("call", snap.ID.String()).Msg("call-ended hook panicked")
					}
				}()
				h(ctx, &snap)
			}()
		}
	})
}

// checkCanStartCall answers 402 payment_required / 429 quota_exceeded when
// the org's plan does not allow a new call right now.
func (s *server) checkCanStartCall(ctx context.Context, orgID uuid.UUID) error {
	if s.d.Entitlements == nil {
		return nil
	}
	ok, reason, err := s.d.Entitlements.CanStartCall(ctx, orgID)
	if err != nil {
		return fmt.Errorf("entitlements: %w", err)
	}
	if ok {
		return nil
	}
	return entitlementError(reason)
}

// entitlementError maps a CanStartCall reason ("payment_required:…" or
// "quota_exceeded:…") to an API error.
func entitlementError(reason string) error {
	code, msg, _ := strings.Cut(reason, ":")
	msg = strings.TrimSpace(msg)
	switch code {
	case "payment_required":
		if msg == "" {
			msg = "subscription is not active"
		}
		return &apiError{status: http.StatusPaymentRequired, code: "payment_required", message: msg}
	default:
		if msg == "" {
			msg = reason
		}
		if msg == "" {
			msg = "plan quota exceeded"
		}
		return &apiError{status: http.StatusTooManyRequests, code: "quota_exceeded", message: msg}
	}
}

// asInvalidRef turns a 404 on a referenced entity into a 400 on the field.
func asInvalidRef(err error, field string) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.status == http.StatusNotFound {
		return errInvalid("%s does not exist", field)
	}
	return err
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Call.Stats(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("stats: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) statsDaily(w http.ResponseWriter, r *http.Request) {
	days, err := queryInt(r, "days", 14, 1, 366)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	series, err := s.d.Call.DailySeries(r.Context(), claimsOf(r).OrgID, days)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("daily series: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(series, len(series)))
}

func (s *server) ws(w http.ResponseWriter, r *http.Request) {
	if s.d.Live == nil {
		s.writeErr(w, r, errNotConfigured("live hub"))
		return
	}
	orgID := claimsOf(r).OrgID
	s.d.Live.ServeWS(w, r, orgID, func(ctx context.Context) []domain.Call {
		calls, err := s.d.Call.ListActiveCalls(ctx, orgID)
		if err != nil {
			s.log.Error().Err(err).Str("orgId", orgID.String()).Msg("ws: list active calls")
			return []domain.Call{}
		}
		if calls == nil {
			calls = []domain.Call{}
		}
		return calls
	})
}
