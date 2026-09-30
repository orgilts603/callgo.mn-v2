package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestEnsureNumberProvisionedCreatesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	n := testNumber()

	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))
	require.NotEmpty(t, n.InboundTrunkID)
	require.NotEmpty(t, n.OutboundTrunkID)
	require.NotEmpty(t, n.DispatchRuleID)
	assert.Len(t, f.inbound, 1)
	assert.Len(t, f.outbound, 1)
	require.Len(t, f.rules, 1)
	rule := f.rules[n.DispatchRuleID]
	assert.Equal(t, []string{n.InboundTrunkID}, rule.TrunkIds)
	assert.Equal(t, "callgo", rule.RoomConfig.Agents[0].AgentName)

	// Second run with IDs set: updates in place, creates nothing.
	ids := [3]string{n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID}
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))
	assert.Equal(t, ids, [3]string{n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID})
	assert.Equal(t, 1, f.count("CreateSIPInboundTrunk"))
	assert.Equal(t, 1, f.count("UpdateSIPInboundTrunk"))
	assert.Equal(t, 1, f.count("UpdateSIPOutboundTrunk"))
	assert.Equal(t, 1, f.count("UpdateSIPDispatchRule"))

	// A copy without IDs (e.g. DB row lost them) finds the resources by name.
	fresh := testNumber()
	require.NoError(t, c.EnsureNumberProvisioned(ctx, fresh))
	assert.Equal(t, ids, [3]string{fresh.InboundTrunkID, fresh.OutboundTrunkID, fresh.DispatchRuleID})
	assert.Len(t, f.inbound, 1)
	assert.Len(t, f.outbound, 1)
	assert.Len(t, f.rules, 1)
}

func TestEnsureNumberProvisionedStaleIDsRecreate(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	n := testNumber()
	n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID = "ST_gone1", "ST_gone2", "SDR_gone"

	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))
	assert.NotEqual(t, "ST_gone1", n.InboundTrunkID)
	assert.NotEqual(t, "ST_gone2", n.OutboundTrunkID)
	assert.NotEqual(t, "SDR_gone", n.DispatchRuleID)
	assert.Contains(t, f.inbound, n.InboundTrunkID)
	assert.Equal(t, []string{n.InboundTrunkID}, f.rules[n.DispatchRuleID].TrunkIds)
}

func TestEnsureNumberProvisionedRespectsDirections(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	n := testNumber()
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))

	n.AllowOutbound = false
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))
	assert.Empty(t, n.OutboundTrunkID)
	assert.Empty(t, f.outbound)
	assert.NotEmpty(t, n.InboundTrunkID)

	n.AllowInbound = false
	n.AllowOutbound = true
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))
	assert.Empty(t, n.InboundTrunkID)
	assert.Empty(t, n.DispatchRuleID)
	assert.Empty(t, f.inbound)
	assert.Empty(t, f.rules)
	assert.NotEmpty(t, n.OutboundTrunkID)
}

func TestEnsureNumberProvisionedErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	require.ErrorIs(t, c.EnsureNumberProvisioned(ctx, &domain.SIPNumber{}), domain.ErrInvalid)

	boom := twirp.NewError(twirp.Internal, "boom")
	f.failOn["CreateSIPOutboundTrunk"] = boom
	err := c.EnsureNumberProvisioned(ctx, testNumber())
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "create outbound trunk")
}

func TestDeprovisionNumber(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	n := testNumber()
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))

	require.NoError(t, c.DeprovisionNumber(ctx, n))
	assert.Empty(t, f.inbound)
	assert.Empty(t, f.outbound)
	assert.Empty(t, f.rules)
	assert.Empty(t, n.InboundTrunkID+n.OutboundTrunkID+n.DispatchRuleID)

	// Already gone (stale IDs): not an error.
	n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID = "ST_x", "ST_y", "SDR_z"
	require.NoError(t, c.DeprovisionNumber(ctx, n))

	// Without IDs: found by name.
	n2 := testNumber()
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n2))
	require.NoError(t, c.DeprovisionNumber(ctx, testNumber()))
	assert.Empty(t, f.inbound)
	assert.Empty(t, f.outbound)
	assert.Empty(t, f.rules)

	// Other errors surface.
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n2))
	f.failOn["DeleteSIPTrunk"] = twirp.NewError(twirp.Unavailable, "down")
	require.Error(t, c.DeprovisionNumber(ctx, n2))
}

func TestDialAnswered(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	req := dialRequest()
	req.RoomName = ""

	res, err := c.Dial(ctx, req)
	require.NoError(t, err)
	assert.True(t, res.Answered)
	assert.Empty(t, res.Error)
	assert.NotEmpty(t, res.ParticipantID)
	assert.Equal(t, "SCL_1", res.SIPCallID)
	assert.Equal(t, domain.StatusActive, DialStatus(res))

	room := "call-" + req.CallID.String()
	assert.Contains(t, f.rooms, room)
	assert.Equal(t, []string{"CreateRoom", "CreateDispatch", "CreateSIPParticipant"}, f.calls)
	require.Len(t, f.dispatch, 1)
	assert.Equal(t, "callgo", f.dispatch[0].AgentName)
	assert.Equal(t, room, f.dispatch[0].Room)

	var md map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.dispatch[0].Metadata), &md))
	assert.Equal(t, req.CallID.String(), md["callId"])
	assert.Equal(t, "outbound", md["direction"])
	assert.Equal(t, "c-1", md["campaignId"])
	assert.Equal(t, f.dispatch[0].Metadata, f.sipReqs[0].ParticipantMetadata)
	assert.Equal(t, f.dispatch[0].Metadata, f.rooms[room].Metadata)
	assert.Equal(t, room, f.sipReqs[0].RoomName)
}

func TestDialOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		err     error
		status  domain.CallStatus
		wantErr bool
	}{
		{"busy", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_BUSY_HERE, "Busy Here")), domain.StatusBusy, false},
		{"no answer", twirp.NewError(twirp.Canceled, "sip request timed out"), domain.StatusNoAnswer, false},
		{"rejected number", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_NOTFOUND, "")), domain.StatusFailed, false},
		{"infrastructure", twirp.NewError(twirp.Unavailable, "sip service not connected"), domain.StatusFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLiveKit()
			f.dialErr = tc.err
			c := newFakeClient(f)
			req := dialRequest()
			res, err := c.Dial(ctx, req)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, res.Answered)
			assert.Equal(t, tc.status, DialStatus(res), res.Error)
			assert.Empty(t, f.rooms, "room cleaned up after failure")
		})
	}
}

func TestDialPreconditions(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)

	req := dialRequest()
	req.FromNumber.OutboundTrunkID = ""
	res, err := c.Dial(ctx, req)
	require.ErrorIs(t, err, domain.ErrInvalid)
	assert.Equal(t, domain.StatusFailed, DialStatus(res))
	assert.Empty(t, f.calls, "nothing sent to LiveKit")

	f.failOn["CreateDispatch"] = twirp.NewError(twirp.Internal, "no agents")
	res, err = c.Dial(ctx, dialRequest())
	require.Error(t, err)
	assert.Equal(t, domain.StatusFailed, DialStatus(res))
	assert.Empty(t, f.rooms, "room cleaned up after dispatch failure")
	assert.Zero(t, f.count("CreateSIPParticipant"))

	f.failOn["CreateRoom"] = twirp.NewError(twirp.Internal, "down")
	_, err = c.Dial(ctx, dialRequest())
	require.Error(t, err)
}

func TestDialCancelledContextIsInfrastructureError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeLiveKit()
	f.dialErr = context.Canceled
	c := newFakeClient(f)
	cancel()
	res, err := c.Dial(ctx, dialRequest())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, domain.StatusFailed, DialStatus(res))
	assert.Empty(t, f.rooms, "cleanup runs even with a cancelled context")
}

func TestHangup(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	f.rooms["call-x"] = &lkproto.Room{Name: "call-x"}
	require.NoError(t, c.Hangup(ctx, "call-x"))
	assert.Empty(t, f.rooms)
	require.NoError(t, c.Hangup(ctx, "call-x"), "already gone is fine")
	require.ErrorIs(t, c.Hangup(ctx, ""), domain.ErrInvalid)

	f.failOn["DeleteRoom"] = twirp.NewError(twirp.Unavailable, "down")
	require.Error(t, c.Hangup(ctx, "call-y"))
}

func TestTransferCall(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newFakeClient(f)
	res, err := c.Dial(ctx, dialRequest())
	require.NoError(t, err)
	room := "call-" + dialRequest().CallID.String()
	f.parts[room] = append([]*lkproto.ParticipantInfo{{Sid: "PA_agent", Identity: "agent-1", Kind: lkproto.ParticipantInfo_AGENT}}, f.parts[room]...)

	// By SID (what Dial returned / Call.ParticipantID stores).
	require.NoError(t, c.TransferCall(ctx, room, res.ParticipantID, "+97611112222"))
	// By identity.
	require.NoError(t, c.TransferCall(ctx, room, "sip-+97699112233", "tel:+97611112222"))
	// Empty: first SIP participant.
	require.NoError(t, c.TransferCall(ctx, room, "", "+97611112222"))
	require.Len(t, f.transfers, 3)
	for _, tr := range f.transfers {
		assert.Equal(t, "sip-+97699112233", tr.ParticipantIdentity)
		assert.Equal(t, "tel:+97611112222", tr.TransferTo)
		assert.True(t, tr.PlayDialtone)
		assert.Equal(t, room, tr.RoomName)
	}

	require.ErrorIs(t, c.TransferCall(ctx, room, "PA_unknown", "+976"), domain.ErrNotFound)
	require.ErrorIs(t, c.TransferCall(ctx, "call-missing", "", "+976"), domain.ErrNotFound)
	require.ErrorIs(t, c.TransferCall(ctx, room, "", ""), domain.ErrInvalid)

	f.failOn["TransferSIPParticipant"] = twirp.NewError(twirp.NotFound, "participant gone")
	require.ErrorIs(t, c.TransferCall(ctx, room, "sip-x", "+976"), domain.ErrNotFound)
	f.failOn["TransferSIPParticipant"] = errors.New("boom")
	err = c.TransferCall(ctx, room, "sip-x", "+976")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrNotFound)
}

func TestListActiveRooms(t *testing.T) {
	f := newFakeLiveKit()
	c := newFakeClient(f)
	for _, n := range []string{"call-11111111-1111-1111-1111-111111111111", "call_+97699112233_abc", "other-room", "callback"} {
		f.rooms[n] = &lkproto.Room{Name: n}
	}
	rooms, err := c.ListActiveRooms(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"call-11111111-1111-1111-1111-111111111111", "call_+97699112233_abc"}, rooms)

	f.failOn["ListRooms"] = twirp.NewError(twirp.Unavailable, "down")
	_, err = c.ListActiveRooms(context.Background())
	require.Error(t, err)
}
