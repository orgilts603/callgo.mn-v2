package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	lkauth "github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// LiveKit SIP participant attributes.
const (
	attrSIPPhone      = "sip.phoneNumber"
	attrSIPTrunkPhone = "sip.trunkPhoneNumber"
	attrSIPCallID     = "sip.callID"
	attrSIPCallStatus = "sip.callStatus"

	callRoomPrefix = "call-"
)

func (s *server) livekitWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	ev, err := webhook.ReceiveWebhookEvent(r, lkauth.NewSimpleKeyProvider(s.cfg.LiveKitAPIKey, s.cfg.LiveKitAPISecret))
	if err != nil {
		s.log.Warn().Err(err).Msg("rejected livekit webhook")
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	// LiveKit retries non-2xx responses; processing errors are logged, not returned.
	if err := s.handleWebhook(r.Context(), ev); err != nil {
		s.log.Error().Err(err).Str("event", ev.GetEvent()).Str("room", ev.GetRoom().GetName()).Msg("handle livekit webhook")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleWebhook(ctx context.Context, ev *livekit.WebhookEvent) error {
	switch ev.GetEvent() {
	case webhook.EventRoomStarted:
		return s.onRoomStarted(ctx, ev.GetRoom())
	case webhook.EventParticipantJoined:
		return s.onParticipantJoined(ctx, ev.GetRoom().GetName(), ev.GetParticipant())
	case webhook.EventParticipantLeft, webhook.EventParticipantConnectionAborted:
		return s.onParticipantLeft(ctx, ev.GetRoom().GetName(), ev.GetParticipant())
	case webhook.EventRoomFinished:
		return s.onRoomFinished(ctx, ev.GetRoom().GetName())
	case webhook.EventEgressEnded:
		return s.onEgressEnded(ctx, ev.GetEgressInfo())
	}
	return nil
}

// callByRoom returns (nil, nil) when no call exists for the room.
func (s *server) callByRoom(ctx context.Context, room string) (*domain.Call, error) {
	if room == "" {
		return nil, nil
	}
	c, err := s.d.Call.GetCallByRoom(ctx, room)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get call by room %s: %w", room, err)
	}
	return c, nil
}

// sipNumberByNumber resolves a DID (any org); (nil, nil) when unknown.
func (s *server) sipNumberByNumber(ctx context.Context, number string) (*domain.SIPNumber, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, nil
	}
	candidates := []string{number}
	if norm, ok := normalizePhone(number); ok && norm != number {
		candidates = append(candidates, norm)
	}
	if !strings.HasPrefix(number, "+") {
		candidates = append(candidates, "+"+strings.TrimPrefix(number, "00"))
	}
	for _, c := range candidates {
		n, err := s.d.SIPNumber.GetSIPNumberByNumber(ctx, c)
		if err == nil && n != nil {
			return n, nil
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("get sip number by number: %w", err)
		}
	}
	return nil, nil
}

// createInboundCall inserts a ringing inbound call for a LiveKit room and
// publishes call.started.
func (s *server) createInboundCall(ctx context.Context, room string, num *domain.SIPNumber, from, to string) (*domain.Call, error) {
	now := s.now()
	if to == "" {
		to = num.Number
	}
	c := &domain.Call{
		ID:             uuid.New(),
		OrgID:          num.OrgID,
		SIPNumberID:    &num.ID,
		AgentProfileID: num.AgentProfileID,
		Direction:      domain.DirectionInbound,
		Status:         domain.StatusRinging,
		FromNumber:     from,
		ToNumber:       to,
		RoomName:       room,
		StartedAt:      now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.linkContact(ctx, c)
	if err := s.d.Call.CreateCall(ctx, c); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			// Raced with another creator (webhook vs. agent bootstrap).
			if existing, gerr := s.callByRoom(ctx, room); gerr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("create inbound call: %w", err)
	}
	s.publishCall(ctx, domain.EventCallStarted, c)
	return c, nil
}

// customerNumber is the remote party's number.
func customerNumber(c *domain.Call) string {
	if c.Direction == domain.DirectionOutbound {
		return c.ToNumber
	}
	return c.FromNumber
}

// linkContact sets ContactID from the customer's number when possible.
func (s *server) linkContact(ctx context.Context, c *domain.Call) bool {
	phone := customerNumber(c)
	if c.ContactID != nil || phone == "" {
		return false
	}
	ct, err := s.d.Contact.GetContactByPhone(ctx, c.OrgID, phone)
	if err != nil || ct == nil || ct.OrgID != c.OrgID {
		return false
	}
	c.ContactID = &ct.ID
	return true
}

type roomMeta struct {
	SIPNumberID string `json:"sipNumberId"`
	CallID      string `json:"callId"`
	Direction   string `json:"direction"`
}

func (s *server) onRoomStarted(ctx context.Context, room *livekit.Room) error {
	name := room.GetName()
	if !strings.HasPrefix(name, callRoomPrefix) {
		return nil
	}
	existing, err := s.callByRoom(ctx, name)
	if err != nil || existing != nil {
		return err
	}
	var meta roomMeta
	if md := room.GetMetadata(); md != "" {
		_ = json.Unmarshal([]byte(md), &meta)
	}
	if meta.Direction == string(domain.DirectionOutbound) {
		return nil // outbound calls are created by the dialer before the room exists
	}
	numID, err := uuid.Parse(meta.SIPNumberID)
	if err != nil {
		// Org unknown until the SIP participant joins (trunk number attribute).
		s.log.Debug().Str("room", name).Msg("room_started without sipNumberId; deferring call creation")
		return nil
	}
	num, err := s.d.SIPNumber.GetSIPNumber(ctx, numID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("get sip number: %w", err)
	}
	_, err = s.createInboundCall(ctx, name, num, "", "")
	return err
}

func isSIPParticipant(p *livekit.ParticipantInfo) bool {
	if p == nil {
		return false
	}
	return p.GetKind() == livekit.ParticipantInfo_SIP || p.GetAttributes()[attrSIPPhone] != ""
}

func (s *server) onParticipantJoined(ctx context.Context, room string, p *livekit.ParticipantInfo) error {
	if !isSIPParticipant(p) {
		return nil
	}
	attrs := p.GetAttributes()
	phone, trunk := attrs[attrSIPPhone], attrs[attrSIPTrunkPhone]
	if n, ok := normalizePhone(phone); ok {
		phone = n
	}
	if n, ok := normalizePhone(trunk); ok {
		trunk = n
	}
	call, err := s.callByRoom(ctx, room)
	if err != nil {
		return err
	}
	var num *domain.SIPNumber
	if trunk != "" {
		if num, err = s.sipNumberByNumber(ctx, trunk); err != nil {
			return err
		}
	}
	created := false
	if call == nil {
		if !strings.HasPrefix(room, callRoomPrefix) {
			return nil
		}
		if num == nil {
			s.log.Warn().Str("room", room).Str("trunk", trunk).Msg("SIP participant for unknown number; call not tracked")
			return nil
		}
		if call, err = s.createInboundCall(ctx, room, num, phone, trunk); err != nil {
			return err
		}
		created = true
	}
	if call.Status.IsTerminal() {
		return nil
	}
	if call.Direction == domain.DirectionOutbound {
		if call.ToNumber == "" {
			call.ToNumber = phone
		}
		if call.FromNumber == "" {
			call.FromNumber = trunk
		}
	} else {
		if call.FromNumber == "" {
			call.FromNumber = phone
		}
		if call.ToNumber == "" {
			call.ToNumber = trunk
		}
	}
	if num != nil && num.OrgID == call.OrgID {
		if call.SIPNumberID == nil {
			call.SIPNumberID = &num.ID
		}
		if call.AgentProfileID == nil {
			call.AgentProfileID = num.AgentProfileID
		}
	}
	s.linkContact(ctx, call)
	if id := p.GetIdentity(); id != "" {
		call.ParticipantID = id
	}
	if sc := attrs[attrSIPCallID]; sc != "" {
		call.SIPCallID = sc
	}
	typ := domain.EventCallUpdated
	// Outbound SIP participants join while still dialing; only "active" (or
	// no status, as for inbound) means the callee picked up.
	if st := attrs[attrSIPCallStatus]; (st == "" || st == "active") && call.Status != domain.StatusActive {
		now := s.now()
		call.Status = domain.StatusActive
		if call.AnsweredAt == nil {
			call.AnsweredAt = &now
		}
		typ = domain.EventCallAnswered
	} else if created {
		return nil // call.started already published and nothing else changed
	}
	call.UpdatedAt = s.now()
	if err := s.d.Call.UpdateCall(ctx, call); err != nil {
		return fmt.Errorf("update call: %w", err)
	}
	s.publishCall(ctx, typ, call)
	return nil
}

func (s *server) onParticipantLeft(ctx context.Context, room string, p *livekit.ParticipantInfo) error {
	if !isSIPParticipant(p) {
		return nil
	}
	call, err := s.callByRoom(ctx, room)
	if err != nil || call == nil || call.Status.IsTerminal() {
		return err
	}
	answered := call.Status == domain.StatusActive || call.AnsweredAt != nil
	o := callOutcome{}
	switch p.GetDisconnectReason() {
	case livekit.DisconnectReason_USER_REJECTED:
		if !answered {
			o.Status, o.EndReason = domain.StatusBusy, "busy"
		}
	case livekit.DisconnectReason_USER_UNAVAILABLE:
		if !answered {
			o.Status, o.EndReason = domain.StatusNoAnswer, "no_answer"
		}
	case livekit.DisconnectReason_SIP_TRUNK_FAILURE, livekit.DisconnectReason_JOIN_FAILURE, livekit.DisconnectReason_MEDIA_FAILURE:
		if !answered {
			o.Status, o.EndReason = domain.StatusFailed, "failed"
		}
	case livekit.DisconnectReason_PARTICIPANT_REMOVED, livekit.DisconnectReason_ROOM_DELETED, livekit.DisconnectReason_ROOM_CLOSED:
		if answered {
			o.EndReason = "hangup_agent"
		}
	}
	if answered && o.EndReason == "" {
		o.EndReason = "hangup_customer"
	}
	_, err = s.finalizeCall(ctx, call, o)
	return err
}

func (s *server) onRoomFinished(ctx context.Context, room string) error {
	call, err := s.callByRoom(ctx, room)
	if err != nil || call == nil || call.Status.IsTerminal() {
		return err
	}
	o := callOutcome{}
	if call.Status == domain.StatusActive || call.AnsweredAt != nil {
		o.EndReason = "hangup_agent"
	}
	_, err = s.finalizeCall(ctx, call, o)
	return err
}

func (s *server) onEgressEnded(ctx context.Context, eg *livekit.EgressInfo) error {
	if eg == nil {
		return nil
	}
	call, err := s.callByRoom(ctx, eg.GetRoomName())
	if err != nil || call == nil {
		return err
	}
	loc := ""
	for _, f := range eg.GetFileResults() {
		if l := firstNonEmpty(f.GetLocation(), f.GetFilename()); l != "" {
			loc = l
			break
		}
	}
	if loc == "" {
		//nolint:staticcheck // legacy single-file result, still sent by older egress
		if f := eg.GetFile(); f != nil {
			loc = firstNonEmpty(f.GetLocation(), f.GetFilename())
		}
	}
	if loc == "" {
		for _, sg := range eg.GetSegmentResults() {
			if l := firstNonEmpty(sg.GetPlaylistLocation(), sg.GetPlaylistName()); l != "" {
				loc = l
				break
			}
		}
	}
	if loc == "" {
		return nil
	}
	call.RecordingURL = loc
	call.UpdatedAt = s.now()
	if err := s.d.Call.UpdateCall(ctx, call); err != nil {
		return fmt.Errorf("update call recording: %w", err)
	}
	s.publishCall(ctx, domain.EventCallUpdated, call)
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
