package livekit

import (
	"testing"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestRoomNameHelpers(t *testing.T) {
	id := uuid.New()
	room := RoomNameForCall(id)
	assert.Equal(t, "call-"+id.String(), room)

	got, ok := CallIDFromRoom(room)
	assert.True(t, ok)
	assert.Equal(t, id, got)

	got, ok = CallIDFromRoom("outbound-" + id.String())
	assert.True(t, ok, "any prefix")
	assert.Equal(t, id, got)

	for _, bad := range []string{"", "call-", "call-in_+97699112233_abc123", "call-not-a-uuid-at-all-xxxxxxxxxxxxxxxxxxxx", "call-" + uuid.Nil.String()} {
		_, ok := CallIDFromRoom(bad)
		assert.False(t, ok, bad)
	}
}

func TestSIPAttributes(t *testing.T) {
	inbound := &lkproto.ParticipantInfo{
		Kind: lkproto.ParticipantInfo_SIP,
		Attributes: map[string]string{
			"sip.phoneNumber":      "+97699112233",
			"sip.trunkPhoneNumber": "+97677001234",
			"sip.callID":           "SCL_abc",
			"sip.callStatus":       "active",
			"sip.trunkID":          "ST_in",
			"sip.ruleID":           "SDR_1",
			AttrSIPNumberID:        "num-1",
		},
	}
	from, to, callID := SIPAttributes(inbound)
	assert.Equal(t, "+97699112233", from)
	assert.Equal(t, "+97677001234", to)
	assert.Equal(t, "SCL_abc", callID)
	sp := ParseSIPParticipant(inbound)
	assert.True(t, sp.IsSIP)
	assert.Equal(t, "inbound", sp.Direction)
	assert.Equal(t, "active", sp.CallStatus)
	assert.Equal(t, "ST_in", sp.TrunkID)
	assert.Equal(t, "SDR_1", sp.RuleID)
	assert.Equal(t, "num-1", sp.SIPNumberID)

	outbound := &lkproto.ParticipantInfo{
		Kind: lkproto.ParticipantInfo_SIP,
		Attributes: map[string]string{
			"sip.phoneNumber":      "+97699112233",
			"sip.trunkPhoneNumber": "+97677001234",
			"sip.callID":           "SCL_out",
			"sip.callStatus":       "ringing",
			AttrDirection:          "outbound",
			AttrCallID:             "call-uuid",
		},
	}
	from, to, callID = SIPAttributes(outbound)
	assert.Equal(t, "+97677001234", from)
	assert.Equal(t, "+97699112233", to)
	assert.Equal(t, "SCL_out", callID)
	assert.Equal(t, "call-uuid", ParseSIPParticipant(outbound).CallID)

	agent := &lkproto.ParticipantInfo{Kind: lkproto.ParticipantInfo_AGENT}
	sp = ParseSIPParticipant(agent)
	assert.False(t, sp.IsSIP)
	assert.Empty(t, sp.Direction)

	from, to, callID = SIPAttributes(nil)
	assert.Empty(t, from+to+callID)
}

func TestRoomMetadata(t *testing.T) {
	assert.Equal(t, "x", RoomMetadata(&lkproto.Room{Metadata: `{"callId":"x"}`})["callId"])
	assert.Empty(t, RoomMetadata(&lkproto.Room{Metadata: `not json`}))
	assert.Empty(t, RoomMetadata(nil))
}
