package livekit

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/rs/zerolog"
	"github.com/twitchtv/twirp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func testLogger() zerolog.Logger { return zerolog.New(io.Discard) }

// fakeLiveKit is an in-memory stand-in for the LiveKit SIP, room and agent
// dispatch APIs. It implements sipAPI, roomAPI and dispatchAPI.
type fakeLiveKit struct {
	mu        sync.Mutex
	seq       int
	inbound   map[string]*lkproto.SIPInboundTrunkInfo
	outbound  map[string]*lkproto.SIPOutboundTrunkInfo
	rules     map[string]*lkproto.SIPDispatchRuleInfo
	rooms     map[string]*lkproto.Room
	parts     map[string][]*lkproto.ParticipantInfo
	calls     []string // method log
	dispatch  []*lkproto.CreateAgentDispatchRequest
	sipReqs   []*lkproto.CreateSIPParticipantRequest
	transfers []*lkproto.TransferSIPParticipantRequest

	// dialErr is returned by CreateSIPParticipant when set.
	dialErr error
	// failOn makes the named method fail with this error.
	failOn map[string]error
}

func newFakeLiveKit() *fakeLiveKit {
	return &fakeLiveKit{
		inbound:  map[string]*lkproto.SIPInboundTrunkInfo{},
		outbound: map[string]*lkproto.SIPOutboundTrunkInfo{},
		rules:    map[string]*lkproto.SIPDispatchRuleInfo{},
		rooms:    map[string]*lkproto.Room{},
		parts:    map[string][]*lkproto.ParticipantInfo{},
		failOn:   map[string]error{},
	}
}

func (f *fakeLiveKit) enter(method string) error {
	f.calls = append(f.calls, method)
	return f.failOn[method]
}

func (f *fakeLiveKit) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s%d", prefix, f.seq)
}

func (f *fakeLiveKit) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

func notFound(what string) error { return twirp.NewError(twirp.NotFound, what+" does not exist") }

// --- sipAPI ---------------------------------------------------------------

func (f *fakeLiveKit) CreateSIPInboundTrunk(_ context.Context, in *lkproto.CreateSIPInboundTrunkRequest) (*lkproto.SIPInboundTrunkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateSIPInboundTrunk"); err != nil {
		return nil, err
	}
	t := proto.CloneOf(in.Trunk)
	t.SipTrunkId = f.id("ST_in")
	f.inbound[t.SipTrunkId] = t
	return t, nil
}

func (f *fakeLiveKit) UpdateSIPInboundTrunk(_ context.Context, in *lkproto.UpdateSIPInboundTrunkRequest) (*lkproto.SIPInboundTrunkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UpdateSIPInboundTrunk"); err != nil {
		return nil, err
	}
	if _, ok := f.inbound[in.SipTrunkId]; !ok {
		return nil, notFound("trunk")
	}
	t := proto.CloneOf(in.GetReplace())
	t.SipTrunkId = in.SipTrunkId
	f.inbound[t.SipTrunkId] = t
	return t, nil
}

func (f *fakeLiveKit) ListSIPInboundTrunk(_ context.Context, in *lkproto.ListSIPInboundTrunkRequest) (*lkproto.ListSIPInboundTrunkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListSIPInboundTrunk"); err != nil {
		return nil, err
	}
	res := &lkproto.ListSIPInboundTrunkResponse{}
	for _, t := range f.inbound {
		if len(in.Numbers) == 0 || slices.ContainsFunc(t.Numbers, func(n string) bool { return slices.Contains(in.Numbers, n) }) {
			res.Items = append(res.Items, t)
		}
	}
	return res, nil
}

func (f *fakeLiveKit) CreateSIPOutboundTrunk(_ context.Context, in *lkproto.CreateSIPOutboundTrunkRequest) (*lkproto.SIPOutboundTrunkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateSIPOutboundTrunk"); err != nil {
		return nil, err
	}
	t := proto.CloneOf(in.Trunk)
	t.SipTrunkId = f.id("ST_out")
	f.outbound[t.SipTrunkId] = t
	return t, nil
}

func (f *fakeLiveKit) UpdateSIPOutboundTrunk(_ context.Context, in *lkproto.UpdateSIPOutboundTrunkRequest) (*lkproto.SIPOutboundTrunkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UpdateSIPOutboundTrunk"); err != nil {
		return nil, err
	}
	if _, ok := f.outbound[in.SipTrunkId]; !ok {
		return nil, notFound("trunk")
	}
	t := proto.CloneOf(in.GetReplace())
	t.SipTrunkId = in.SipTrunkId
	f.outbound[t.SipTrunkId] = t
	return t, nil
}

func (f *fakeLiveKit) ListSIPOutboundTrunk(_ context.Context, in *lkproto.ListSIPOutboundTrunkRequest) (*lkproto.ListSIPOutboundTrunkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListSIPOutboundTrunk"); err != nil {
		return nil, err
	}
	res := &lkproto.ListSIPOutboundTrunkResponse{}
	for _, t := range f.outbound {
		if len(in.Numbers) == 0 || slices.ContainsFunc(t.Numbers, func(n string) bool { return slices.Contains(in.Numbers, n) }) {
			res.Items = append(res.Items, t)
		}
	}
	return res, nil
}

func (f *fakeLiveKit) DeleteSIPTrunk(_ context.Context, in *lkproto.DeleteSIPTrunkRequest) (*lkproto.SIPTrunkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteSIPTrunk"); err != nil {
		return nil, err
	}
	if _, ok := f.inbound[in.SipTrunkId]; ok {
		delete(f.inbound, in.SipTrunkId)
		return &lkproto.SIPTrunkInfo{SipTrunkId: in.SipTrunkId}, nil
	}
	if _, ok := f.outbound[in.SipTrunkId]; ok {
		delete(f.outbound, in.SipTrunkId)
		return &lkproto.SIPTrunkInfo{SipTrunkId: in.SipTrunkId}, nil
	}
	return nil, notFound("trunk")
}

func (f *fakeLiveKit) CreateSIPDispatchRule(_ context.Context, in *lkproto.CreateSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateSIPDispatchRule"); err != nil {
		return nil, err
	}
	r := proto.CloneOf(in.DispatchRule)
	r.SipDispatchRuleId = f.id("SDR_")
	f.rules[r.SipDispatchRuleId] = r
	return r, nil
}

func (f *fakeLiveKit) UpdateSIPDispatchRule(_ context.Context, in *lkproto.UpdateSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UpdateSIPDispatchRule"); err != nil {
		return nil, err
	}
	if _, ok := f.rules[in.SipDispatchRuleId]; !ok {
		return nil, notFound("rule")
	}
	r := proto.CloneOf(in.GetReplace())
	r.SipDispatchRuleId = in.SipDispatchRuleId
	f.rules[r.SipDispatchRuleId] = r
	return r, nil
}

func (f *fakeLiveKit) ListSIPDispatchRule(_ context.Context, in *lkproto.ListSIPDispatchRuleRequest) (*lkproto.ListSIPDispatchRuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListSIPDispatchRule"); err != nil {
		return nil, err
	}
	res := &lkproto.ListSIPDispatchRuleResponse{}
	for _, r := range f.rules {
		if len(in.TrunkIds) == 0 || slices.ContainsFunc(r.TrunkIds, func(id string) bool { return slices.Contains(in.TrunkIds, id) }) {
			res.Items = append(res.Items, r)
		}
	}
	return res, nil
}

func (f *fakeLiveKit) DeleteSIPDispatchRule(_ context.Context, in *lkproto.DeleteSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteSIPDispatchRule"); err != nil {
		return nil, err
	}
	if _, ok := f.rules[in.SipDispatchRuleId]; !ok {
		return nil, notFound("rule")
	}
	delete(f.rules, in.SipDispatchRuleId)
	return &lkproto.SIPDispatchRuleInfo{SipDispatchRuleId: in.SipDispatchRuleId}, nil
}

func (f *fakeLiveKit) CreateSIPParticipant(_ context.Context, in *lkproto.CreateSIPParticipantRequest) (*lkproto.SIPParticipantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateSIPParticipant"); err != nil {
		return nil, err
	}
	f.sipReqs = append(f.sipReqs, in)
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	pid := f.id("PA_")
	f.parts[in.RoomName] = append(f.parts[in.RoomName], &lkproto.ParticipantInfo{
		Sid: pid, Identity: in.ParticipantIdentity, Kind: lkproto.ParticipantInfo_SIP,
		Attributes: map[string]string{"sip.callID": "SCL_1"},
	})
	return &lkproto.SIPParticipantInfo{
		ParticipantId:       pid,
		ParticipantIdentity: in.ParticipantIdentity,
		RoomName:            in.RoomName,
		SipCallId:           "SCL_1",
	}, nil
}

func (f *fakeLiveKit) TransferSIPParticipant(_ context.Context, in *lkproto.TransferSIPParticipantRequest) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("TransferSIPParticipant"); err != nil {
		return nil, err
	}
	f.transfers = append(f.transfers, in)
	return &emptypb.Empty{}, nil
}

// --- roomAPI --------------------------------------------------------------

func (f *fakeLiveKit) CreateRoom(_ context.Context, req *lkproto.CreateRoomRequest) (*lkproto.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateRoom"); err != nil {
		return nil, err
	}
	r := &lkproto.Room{Name: req.Name, Sid: f.id("RM_"), Metadata: req.Metadata}
	f.rooms[req.Name] = r
	return r, nil
}

func (f *fakeLiveKit) DeleteRoom(_ context.Context, req *lkproto.DeleteRoomRequest) (*lkproto.DeleteRoomResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteRoom"); err != nil {
		return nil, err
	}
	if _, ok := f.rooms[req.Room]; !ok {
		return nil, notFound("room")
	}
	delete(f.rooms, req.Room)
	delete(f.parts, req.Room)
	return &lkproto.DeleteRoomResponse{}, nil
}

func (f *fakeLiveKit) ListRooms(_ context.Context, _ *lkproto.ListRoomsRequest) (*lkproto.ListRoomsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListRooms"); err != nil {
		return nil, err
	}
	res := &lkproto.ListRoomsResponse{}
	for _, r := range f.rooms {
		res.Rooms = append(res.Rooms, r)
	}
	slices.SortFunc(res.Rooms, func(a, b *lkproto.Room) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return res, nil
}

func (f *fakeLiveKit) ListParticipants(_ context.Context, req *lkproto.ListParticipantsRequest) (*lkproto.ListParticipantsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListParticipants"); err != nil {
		return nil, err
	}
	if _, ok := f.rooms[req.Room]; !ok {
		return nil, notFound("room")
	}
	return &lkproto.ListParticipantsResponse{Participants: f.parts[req.Room]}, nil
}

// --- dispatchAPI ----------------------------------------------------------

func (f *fakeLiveKit) CreateDispatch(_ context.Context, req *lkproto.CreateAgentDispatchRequest) (*lkproto.AgentDispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateDispatch"); err != nil {
		return nil, err
	}
	f.dispatch = append(f.dispatch, req)
	return &lkproto.AgentDispatch{Id: f.id("AD_"), AgentName: req.AgentName, Room: req.Room, Metadata: req.Metadata}, nil
}

func newFakeClient(f *fakeLiveKit) *Client {
	return newClient(testConfig(), testLogger(), f, f, f)
}
