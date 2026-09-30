package livekit

import (
	"strings"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
)

// Participant attributes set by CallGo on SIP participants (in addition to
// LiveKit's own "sip.*" attributes). Inbound participants get them from the
// dispatch rule, outbound ones from CreateSIPParticipant.
const (
	AttrCallID      = "callgo.callId"
	AttrDirection   = "callgo.direction"
	AttrSIPNumberID = "callgo.sipNumberId"
)

// Resource name prefixes of the LiveKit SIP objects owned by CallGo. They make
// provisioning idempotent: existing objects are found by name.
const (
	inboundTrunkNamePrefix  = "callgo-in-"
	outboundTrunkNamePrefix = "callgo-out-"
	dispatchRuleNamePrefix  = "callgo-rule-"
)

func inboundTrunkName(number string) string  { return inboundTrunkNamePrefix + number }
func outboundTrunkName(number string) string { return outboundTrunkNamePrefix + number }
func dispatchRuleName(number string) string  { return dispatchRuleNamePrefix + number }

// RoomNameForCall returns the room name used for an outbound call with the
// default prefix: "call-<uuid>". Client.RoomName honours a custom prefix.
func RoomNameForCall(id uuid.UUID) string {
	return DefaultRoomPrefix + id.String()
}

// CallIDFromRoom extracts the call UUID from a room named <prefix><uuid>
// (any prefix, e.g. "call-" from RoomNameForCall). Inbound rooms created by
// the dispatch rule ("call-in_<caller>_<random>") carry no call ID and return
// false; resolve those with CallRepository.GetCallByRoom.
func CallIDFromRoom(room string) (uuid.UUID, bool) {
	const uuidLen = 36
	if len(room) < uuidLen {
		return uuid.Nil, false
	}
	tail := room[len(room)-uuidLen:]
	id, err := uuid.Parse(tail)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

// SIPParticipant is the SIP-related view of a LiveKit participant, decoded
// from its attributes.
type SIPParticipant struct {
	// IsSIP reports whether the participant is a SIP participant.
	IsSIP bool
	// Direction is "inbound" or "outbound" (from the callgo.direction
	// attribute; defaults to inbound for SIP participants without it).
	Direction string
	// From / To are the caller and callee numbers from the platform's point
	// of view (inbound: caller → our DID; outbound: our DID → callee).
	From string
	To   string
	// SIPCallID is LiveKit's SIP call ID (sip.callID, "SCL_...").
	SIPCallID string
	// CallStatus is sip.callStatus: "dialing", "ringing", "automation",
	// "active" or "hangup".
	CallStatus string
	TrunkID    string
	RuleID     string
	// CallID / SIPNumberID are CallGo attributes when present.
	CallID      string
	SIPNumberID string
}

// ParseSIPParticipant decodes the SIP attributes of p. p may be nil.
func ParseSIPParticipant(p *lkproto.ParticipantInfo) SIPParticipant {
	attrs := p.GetAttributes()
	sp := SIPParticipant{
		SIPCallID:   attrs[lkproto.AttrSIPCallID],
		CallStatus:  attrs[lkproto.AttrSIPCallStatus],
		TrunkID:     attrs[lkproto.AttrSIPTrunkID],
		RuleID:      attrs[lkproto.AttrSIPDispatchRuleID],
		CallID:      attrs[AttrCallID],
		SIPNumberID: attrs[AttrSIPNumberID],
		Direction:   attrs[AttrDirection],
	}
	sp.IsSIP = p.GetKind() == lkproto.ParticipantInfo_SIP || sp.SIPCallID != ""
	if sp.Direction == "" && sp.IsSIP {
		sp.Direction = "inbound"
	}
	remote := attrs[lkproto.AttrSIPPhoneNumber]
	trunk := attrs[lkproto.AttrSIPTrunkNumber]
	if sp.Direction == "outbound" {
		sp.From, sp.To = trunk, remote
	} else {
		sp.From, sp.To = remote, trunk
	}
	return sp
}

// SIPAttributes returns the caller number, callee number and LiveKit SIP
// call ID of a SIP participant (see ParseSIPParticipant for the full view,
// including sip.callStatus).
func SIPAttributes(p *lkproto.ParticipantInfo) (from, to, callID string) {
	sp := ParseSIPParticipant(p)
	return sp.From, sp.To, sp.SIPCallID
}

// normalizeTransferTarget turns a phone number into a SIP REFER target.
// Values that already carry a "tel:" or "sip:"/"sips:" scheme are kept.
func normalizeTransferTarget(number string) string {
	n := strings.TrimSpace(number)
	lower := strings.ToLower(n)
	if strings.HasPrefix(lower, "tel:") || strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:") {
		return n
	}
	return "tel:" + n
}
