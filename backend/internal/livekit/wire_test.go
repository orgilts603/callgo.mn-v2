package livekit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/xtwirp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// The wire test runs the real server-sdk-go clients (built by NewClient)
// against twirp servers generated from the LiveKit protocol, backed by the
// in-memory fake. It checks auth headers, twirp routing and — most
// importantly — that SIP status details survive the HTTP hop so Dial can
// tell "busy" from "failed".

type wireSIP struct {
	lkproto.SIP // unimplemented methods panic; only the ones below are used
	f           *fakeLiveKit
}

func (w wireSIP) CreateSIPParticipant(ctx context.Context, in *lkproto.CreateSIPParticipantRequest) (*lkproto.SIPParticipantInfo, error) {
	return w.f.CreateSIPParticipant(ctx, in)
}

func (w wireSIP) CreateSIPInboundTrunk(ctx context.Context, in *lkproto.CreateSIPInboundTrunkRequest) (*lkproto.SIPInboundTrunkInfo, error) {
	return w.f.CreateSIPInboundTrunk(ctx, in)
}

func (w wireSIP) ListSIPInboundTrunk(ctx context.Context, in *lkproto.ListSIPInboundTrunkRequest) (*lkproto.ListSIPInboundTrunkResponse, error) {
	return w.f.ListSIPInboundTrunk(ctx, in)
}

func (w wireSIP) CreateSIPDispatchRule(ctx context.Context, in *lkproto.CreateSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error) {
	return w.f.CreateSIPDispatchRule(ctx, in)
}

func (w wireSIP) ListSIPDispatchRule(ctx context.Context, in *lkproto.ListSIPDispatchRuleRequest) (*lkproto.ListSIPDispatchRuleResponse, error) {
	return w.f.ListSIPDispatchRule(ctx, in)
}

func (w wireSIP) ListSIPOutboundTrunk(ctx context.Context, in *lkproto.ListSIPOutboundTrunkRequest) (*lkproto.ListSIPOutboundTrunkResponse, error) {
	return w.f.ListSIPOutboundTrunk(ctx, in)
}

func (w wireSIP) DeleteSIPTrunk(ctx context.Context, in *lkproto.DeleteSIPTrunkRequest) (*lkproto.SIPTrunkInfo, error) {
	return w.f.DeleteSIPTrunk(ctx, in)
}

func (w wireSIP) DeleteSIPDispatchRule(ctx context.Context, in *lkproto.DeleteSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error) {
	return w.f.DeleteSIPDispatchRule(ctx, in)
}

type wireRooms struct {
	lkproto.RoomService
	f *fakeLiveKit
}

func (w wireRooms) CreateRoom(ctx context.Context, req *lkproto.CreateRoomRequest) (*lkproto.Room, error) {
	return w.f.CreateRoom(ctx, req)
}

func (w wireRooms) DeleteRoom(ctx context.Context, req *lkproto.DeleteRoomRequest) (*lkproto.DeleteRoomResponse, error) {
	return w.f.DeleteRoom(ctx, req)
}

func (w wireRooms) ListRooms(ctx context.Context, req *lkproto.ListRoomsRequest) (*lkproto.ListRoomsResponse, error) {
	return w.f.ListRooms(ctx, req)
}

type wireDispatch struct {
	lkproto.AgentDispatchService
	f *fakeLiveKit
}

func (w wireDispatch) CreateDispatch(ctx context.Context, req *lkproto.CreateAgentDispatchRequest) (*lkproto.AgentDispatch, error) {
	return w.f.CreateDispatch(ctx, req)
}

func newWireClient(t *testing.T, f *fakeLiveKit) *Client {
	t.Helper()
	opts := []any{xtwirp.ServerPassErrorDetails()}
	mux := http.NewServeMux()
	for _, srv := range []lkproto.TwirpServer{
		lkproto.NewSIPServer(wireSIP{f: f}, opts...),
		lkproto.NewRoomServiceServer(wireRooms{f: f}, opts...),
		lkproto.NewAgentDispatchServiceServer(wireDispatch{f: f}, opts...),
	} {
		mux.Handle(srv.PathPrefix(), srv)
	}
	var sawAuth bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			sawAuth = true
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		ts.Close()
		assert.True(t, sawAuth, "SDK sent a bearer token")
	})
	cfg := testConfig()
	cfg.URL = ts.URL
	c, err := NewClient(cfg, testLogger())
	require.NoError(t, err)
	return c
}

func TestWireDial(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newWireClient(t, f)

	res, err := c.Dial(ctx, dialRequest())
	require.NoError(t, err)
	assert.True(t, res.Answered)
	assert.Equal(t, "SCL_1", res.SIPCallID)
	require.Len(t, f.sipReqs, 1)
	assert.Equal(t, "sip-+97699112233", f.sipReqs[0].ParticipantIdentity)

	// Busy: SIP 486 travels as twirp metadata and is decoded client side.
	f.dialErr = sipErr(lkproto.SIPStatusCode_SIP_STATUS_BUSY_HERE, "Busy Here")
	req := dialRequest()
	req.CallID[15] ^= 0xff // different room
	res, err = c.Dial(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusBusy, DialStatus(res), res.Error)
	assert.Contains(t, res.Error, "486")

	// Hangup of a room that is already gone: twirp not_found is tolerated.
	require.NoError(t, c.Hangup(ctx, "call-does-not-exist"))

	rooms, err := c.ListActiveRooms(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"call-" + dialRequest().CallID.String()}, rooms)
}

func TestWireProvisionInboundOnly(t *testing.T) {
	ctx := context.Background()
	f := newFakeLiveKit()
	c := newWireClient(t, f)
	n := testNumber()
	n.AllowOutbound = false
	require.NoError(t, c.EnsureNumberProvisioned(ctx, n))
	assert.NotEmpty(t, n.InboundTrunkID)
	assert.NotEmpty(t, n.DispatchRuleID)
	assert.Empty(t, n.OutboundTrunkID)
	require.NoError(t, c.DeprovisionNumber(ctx, n))
	assert.Empty(t, f.inbound)
	assert.Empty(t, f.rules)
}
