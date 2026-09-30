package livekit

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// This file holds pure functions that turn domain values into LiveKit
// protobuf requests. They never touch the network and are unit-tested.

// inboundDispatchMetadata is the agent job metadata of inbound calls. It
// deliberately contains only stable identifiers, so changing a number's agent
// profile does not require re-provisioning (the agent resolves the profile via
// /internal/agent/bootstrap).
type inboundDispatchMetadata struct {
	SIPNumberID string `json:"sipNumberId"`
	Direction   string `json:"direction"`
}

// trunkMetadata is stored on the LiveKit trunk / rule objects for operators.
type trunkMetadata struct {
	SIPNumberID string `json:"sipNumberId"`
	OrgID       string `json:"orgId"`
	Number      string `json:"number"`
	Label       string `json:"label,omitempty"`
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("livekit: encode metadata: %w", err)
	}
	return string(b), nil
}

func numberMetadata(n *domain.SIPNumber) (string, error) {
	return marshalJSON(trunkMetadata{
		SIPNumberID: n.ID.String(),
		OrgID:       n.OrgID.String(),
		Number:      n.Number,
		Label:       n.Label,
	})
}

func validateNumber(n *domain.SIPNumber) error {
	if n == nil {
		return fmt.Errorf("%w: sip number is nil", domain.ErrInvalid)
	}
	if strings.TrimSpace(n.Number) == "" {
		return fmt.Errorf("%w: sip number has no number", domain.ErrInvalid)
	}
	return nil
}

// buildInboundTrunk builds the inbound trunk accepting INVITEs for n.Number
// from Asterisk.
func buildInboundTrunk(cfg Config, n *domain.SIPNumber) (*lkproto.SIPInboundTrunkInfo, error) {
	if err := validateNumber(n); err != nil {
		return nil, err
	}
	md, err := numberMetadata(n)
	if err != nil {
		return nil, err
	}
	return &lkproto.SIPInboundTrunkInfo{
		Name:             inboundTrunkName(n.Number),
		Metadata:         md,
		Numbers:          []string{n.Number},
		AllowedAddresses: append([]string(nil), cfg.SIPInboundAllowedAddresses...),
		AuthUsername:     cfg.SIPInboundAuthUsername,
		AuthPassword:     cfg.SIPInboundAuthPassword,
		KrispEnabled:     false,
	}, nil
}

// buildOutboundTrunk builds the outbound trunk sending calls with caller ID
// n.Number to Asterisk.
func buildOutboundTrunk(cfg Config, n *domain.SIPNumber) (*lkproto.SIPOutboundTrunkInfo, error) {
	if err := validateNumber(n); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.SIPOutboundAddress) == "" {
		return nil, fmt.Errorf("%w: livekit config: SIPOutboundAddress is required for outbound numbers", domain.ErrInvalid)
	}
	transport, err := parseTransport(cfg.SIPOutboundTransport)
	if err != nil {
		return nil, err
	}
	md, err := numberMetadata(n)
	if err != nil {
		return nil, err
	}
	return &lkproto.SIPOutboundTrunkInfo{
		Name:         outboundTrunkName(n.Number),
		Metadata:     md,
		Address:      cfg.SIPOutboundAddress,
		Transport:    transport,
		Numbers:      []string{n.Number},
		AuthUsername: cfg.SIPAuthUsername,
		AuthPassword: cfg.SIPAuthPassword,
	}, nil
}

// buildDispatchRule builds the "individual room per call" dispatch rule bound
// to the inbound trunk, dispatching cfg.AgentName with inbound metadata.
func buildDispatchRule(cfg Config, n *domain.SIPNumber, inboundTrunkID string) (*lkproto.SIPDispatchRuleInfo, error) {
	if err := validateNumber(n); err != nil {
		return nil, err
	}
	if inboundTrunkID == "" {
		return nil, fmt.Errorf("%w: dispatch rule needs an inbound trunk id", domain.ErrInvalid)
	}
	agentMD, err := marshalJSON(inboundDispatchMetadata{
		SIPNumberID: n.ID.String(),
		Direction:   string(domain.DirectionInbound),
	})
	if err != nil {
		return nil, err
	}
	md, err := numberMetadata(n)
	if err != nil {
		return nil, err
	}
	return &lkproto.SIPDispatchRuleInfo{
		Name:     dispatchRuleName(n.Number),
		Metadata: md,
		TrunkIds: []string{inboundTrunkID},
		Rule: &lkproto.SIPDispatchRule{
			Rule: &lkproto.SIPDispatchRule_DispatchRuleIndividual{
				DispatchRuleIndividual: &lkproto.SIPDispatchRuleIndividual{
					RoomPrefix: cfg.inboundRoomPrefix(),
				},
			},
		},
		// Become attributes of the inbound SIP participant.
		Attributes: map[string]string{
			AttrSIPNumberID: n.ID.String(),
			AttrDirection:   string(domain.DirectionInbound),
		},
		RoomConfig: &lkproto.RoomConfiguration{
			// Room metadata lets room_started webhooks resolve the number.
			Metadata:         agentMD,
			DepartureTimeout: roomDepartureTimeout,
			Agents: []*lkproto.RoomAgentDispatch{{
				AgentName: cfg.AgentName,
				Metadata:  agentMD,
			}},
		},
		KrispEnabled: false,
	}, nil
}

// dialRoomName returns the room of an outbound call.
func dialRoomName(cfg Config, req domain.OutboundCallRequest) string {
	if req.RoomName != "" {
		return req.RoomName
	}
	return cfg.RoomPrefix + req.CallID.String()
}

// dialMetadata merges req.Metadata with the fields the agent needs
// (callId, direction, toNumber, fromNumber, sipNumberId, agentProfileId).
// CallGo's keys win over caller-provided ones. The result matches the Python
// agent's JobMetadata (camelCase, unknown keys ignored).
func dialMetadata(req domain.OutboundCallRequest) map[string]any {
	md := make(map[string]any, len(req.Metadata)+6)
	for k, v := range req.Metadata {
		md[k] = v
	}
	md["callId"] = req.CallID.String()
	md["direction"] = string(domain.DirectionOutbound)
	md["toNumber"] = req.ToNumber
	md["fromNumber"] = req.FromNumber.Number
	if req.FromNumber.ID != uuid.Nil {
		md["sipNumberId"] = req.FromNumber.ID.String()
	}
	if req.AgentProfile.ID != uuid.Nil {
		md["agentProfileId"] = req.AgentProfile.ID.String()
	}
	return md
}

// participantName picks a display name for the callee.
func participantName(req domain.OutboundCallRequest) string {
	for _, k := range []string{"contactName", "name"} {
		if s, ok := req.Metadata[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return req.ToNumber
}

// buildCreateRoom pre-creates the outbound call room.
func buildCreateRoom(room, metadata string) *lkproto.CreateRoomRequest {
	return &lkproto.CreateRoomRequest{
		Name:             room,
		EmptyTimeout:     roomEmptyTimeout,
		DepartureTimeout: roomDepartureTimeout,
		Metadata:         metadata,
	}
}

// buildAgentDispatch dispatches the CallGo agent into an outbound room.
func buildAgentDispatch(cfg Config, room, metadata string) *lkproto.CreateAgentDispatchRequest {
	return &lkproto.CreateAgentDispatchRequest{
		AgentName: cfg.AgentName,
		Room:      room,
		Metadata:  metadata,
	}
}

// buildSIPParticipant builds the CreateSIPParticipant request of an
// outbound call. metadata is the JSON of dialMetadata(req).
func buildSIPParticipant(cfg Config, req domain.OutboundCallRequest, metadata string) (*lkproto.CreateSIPParticipantRequest, error) {
	if req.FromNumber.OutboundTrunkID == "" {
		return nil, fmt.Errorf("%w: sip number %q has no outbound trunk (provision it first)", domain.ErrInvalid, req.FromNumber.Number)
	}
	to := strings.TrimSpace(req.ToNumber)
	if to == "" {
		return nil, fmt.Errorf("%w: missing destination number", domain.ErrInvalid)
	}
	ring := req.RingTimeout
	if ring <= 0 {
		ring = cfg.RingTimeout
	}
	maxDur := cfg.MaxCallDuration
	if req.AgentProfile.MaxDurationSec > 0 {
		maxDur = time.Duration(req.AgentProfile.MaxDurationSec) * time.Second
	}
	attrs := map[string]string{
		AttrCallID:    req.CallID.String(),
		AttrDirection: string(domain.DirectionOutbound),
	}
	if req.FromNumber.ID != uuid.Nil {
		attrs[AttrSIPNumberID] = req.FromNumber.ID.String()
	}
	return &lkproto.CreateSIPParticipantRequest{
		SipTrunkId:            req.FromNumber.OutboundTrunkID,
		SipCallTo:             to,
		SipNumber:             req.FromNumber.Number,
		RoomName:              dialRoomName(cfg, req),
		ParticipantIdentity:   "sip-" + to,
		ParticipantName:       participantName(req),
		ParticipantMetadata:   metadata,
		ParticipantAttributes: attrs,
		WaitUntilAnswered:     req.WaitUntilAnswered,
		RingingTimeout:        durationpb.New(ring),
		MaxCallDuration:       durationpb.New(maxDur),
		KrispEnabled:          false,
	}, nil
}

// buildTransfer builds a SIP REFER of participant identity to toNumber.
func buildTransfer(room, identity, toNumber string) (*lkproto.TransferSIPParticipantRequest, error) {
	if strings.TrimSpace(toNumber) == "" {
		return nil, fmt.Errorf("%w: missing transfer number", domain.ErrInvalid)
	}
	if room == "" || identity == "" {
		return nil, fmt.Errorf("%w: transfer needs room and participant", domain.ErrInvalid)
	}
	return &lkproto.TransferSIPParticipantRequest{
		RoomName:            room,
		ParticipantIdentity: identity,
		TransferTo:          normalizeTransferTarget(toNumber),
		PlayDialtone:        true,
	}, nil
}
