package livekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// sipAPI is the subset of *lksdk.SIPClient the adapter uses.
type sipAPI interface {
	CreateSIPInboundTrunk(ctx context.Context, in *lkproto.CreateSIPInboundTrunkRequest) (*lkproto.SIPInboundTrunkInfo, error)
	UpdateSIPInboundTrunk(ctx context.Context, in *lkproto.UpdateSIPInboundTrunkRequest) (*lkproto.SIPInboundTrunkInfo, error)
	ListSIPInboundTrunk(ctx context.Context, in *lkproto.ListSIPInboundTrunkRequest) (*lkproto.ListSIPInboundTrunkResponse, error)
	CreateSIPOutboundTrunk(ctx context.Context, in *lkproto.CreateSIPOutboundTrunkRequest) (*lkproto.SIPOutboundTrunkInfo, error)
	UpdateSIPOutboundTrunk(ctx context.Context, in *lkproto.UpdateSIPOutboundTrunkRequest) (*lkproto.SIPOutboundTrunkInfo, error)
	ListSIPOutboundTrunk(ctx context.Context, in *lkproto.ListSIPOutboundTrunkRequest) (*lkproto.ListSIPOutboundTrunkResponse, error)
	DeleteSIPTrunk(ctx context.Context, in *lkproto.DeleteSIPTrunkRequest) (*lkproto.SIPTrunkInfo, error)
	CreateSIPDispatchRule(ctx context.Context, in *lkproto.CreateSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error)
	UpdateSIPDispatchRule(ctx context.Context, in *lkproto.UpdateSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error)
	ListSIPDispatchRule(ctx context.Context, in *lkproto.ListSIPDispatchRuleRequest) (*lkproto.ListSIPDispatchRuleResponse, error)
	DeleteSIPDispatchRule(ctx context.Context, in *lkproto.DeleteSIPDispatchRuleRequest) (*lkproto.SIPDispatchRuleInfo, error)
	CreateSIPParticipant(ctx context.Context, in *lkproto.CreateSIPParticipantRequest) (*lkproto.SIPParticipantInfo, error)
	TransferSIPParticipant(ctx context.Context, in *lkproto.TransferSIPParticipantRequest) (*emptypb.Empty, error)
}

// roomAPI is the subset of *lksdk.RoomServiceClient the adapter uses.
type roomAPI interface {
	CreateRoom(ctx context.Context, req *lkproto.CreateRoomRequest) (*lkproto.Room, error)
	DeleteRoom(ctx context.Context, req *lkproto.DeleteRoomRequest) (*lkproto.DeleteRoomResponse, error)
	ListRooms(ctx context.Context, req *lkproto.ListRoomsRequest) (*lkproto.ListRoomsResponse, error)
	ListParticipants(ctx context.Context, req *lkproto.ListParticipantsRequest) (*lkproto.ListParticipantsResponse, error)
}

// dispatchAPI is the subset of *lksdk.AgentDispatchClient the adapter uses.
type dispatchAPI interface {
	CreateDispatch(ctx context.Context, req *lkproto.CreateAgentDispatchRequest) (*lkproto.AgentDispatch, error)
}

// cleanupTimeout bounds best-effort cleanup calls made after a failure.
const cleanupTimeout = 10 * time.Second

// Client implements domain.Telephony against a LiveKit server with the SIP
// service enabled.
type Client struct {
	cfg      Config
	log      zerolog.Logger
	sip      sipAPI
	rooms    roomAPI
	dispatch dispatchAPI
}

var _ domain.Telephony = (*Client)(nil)

// NewClient validates cfg (applying defaults) and builds the LiveKit API
// clients. It does not contact the server.
func NewClient(cfg Config, log zerolog.Logger) (*Client, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if len(cfg.SIPInboundAllowedAddresses) == 0 && cfg.SIPInboundAuthUsername == "" {
		log.Warn().Msg("livekit: inbound SIP trunks accept INVITEs from any address; set SIPInboundAllowedAddresses to the Asterisk IP")
	}
	return newClient(cfg,
		log.With().Str("component", "livekit").Logger(),
		lksdk.NewSIPClient(cfg.URL, cfg.APIKey, cfg.APISecret),
		lksdk.NewRoomServiceClient(cfg.URL, cfg.APIKey, cfg.APISecret),
		lksdk.NewAgentDispatchServiceClient(cfg.URL, cfg.APIKey, cfg.APISecret),
	), nil
}

func newClient(cfg Config, log zerolog.Logger, s sipAPI, r roomAPI, d dispatchAPI) *Client {
	return &Client{cfg: cfg, log: log, sip: s, rooms: r, dispatch: d}
}

// Config returns the effective configuration (defaults applied).
func (c *Client) Config() Config { return c.cfg }

// RoomName returns the outbound room name of a call with the configured prefix.
func (c *Client) RoomName(callID uuid.UUID) string { return c.cfg.RoomPrefix + callID.String() }

// ParseWebhook verifies and decodes a LiveKit webhook signed with the
// client's API key pair.
func (c *Client) ParseWebhook(r *http.Request) (*lkproto.WebhookEvent, error) {
	return ParseWebhook(r, c.cfg.APIKey, c.cfg.APISecret)
}

// ---------------------------------------------------------------------------
// Provisioning
// ---------------------------------------------------------------------------

// EnsureNumberProvisioned creates or updates the inbound trunk + dispatch
// rule (when n.AllowInbound) and the outbound trunk (when n.AllowOutbound),
// and fills the *ID fields of n. Resources for a disabled direction are
// removed and their IDs cleared. Existing resources are found by stored ID or
// by name, so the call is idempotent.
func (c *Client) EnsureNumberProvisioned(ctx context.Context, n *domain.SIPNumber) error {
	if err := validateNumber(n); err != nil {
		return err
	}
	log := c.log.With().Str("number", n.Number).Str("sipNumberId", n.ID.String()).Logger()

	if n.AllowInbound {
		if err := c.ensureInboundTrunk(ctx, n); err != nil {
			return err
		}
		if err := c.ensureDispatchRule(ctx, n); err != nil {
			return err
		}
	} else if err := c.removeInbound(ctx, n); err != nil {
		return err
	}

	if n.AllowOutbound {
		if err := c.ensureOutboundTrunk(ctx, n); err != nil {
			return err
		}
	} else if err := c.removeOutbound(ctx, n); err != nil {
		return err
	}

	log.Info().
		Str("inboundTrunkId", n.InboundTrunkID).
		Str("outboundTrunkId", n.OutboundTrunkID).
		Str("dispatchRuleId", n.DispatchRuleID).
		Msg("livekit: number provisioned")
	return nil
}

// DeprovisionNumber deletes the dispatch rule and both trunks of n (found by
// ID or by name). Missing resources are ignored. The IDs of n are cleared.
func (c *Client) DeprovisionNumber(ctx context.Context, n *domain.SIPNumber) error {
	if err := validateNumber(n); err != nil {
		return err
	}
	return errors.Join(c.removeInbound(ctx, n), c.removeOutbound(ctx, n))
}

func (c *Client) ensureInboundTrunk(ctx context.Context, n *domain.SIPNumber) error {
	want, err := buildInboundTrunk(c.cfg, n)
	if err != nil {
		return err
	}
	id := n.InboundTrunkID
	if id == "" {
		if id, err = c.findInboundTrunk(ctx, n.Number); err != nil {
			return err
		}
	}
	if id != "" {
		got, err := c.sip.UpdateSIPInboundTrunk(ctx, &lkproto.UpdateSIPInboundTrunkRequest{
			SipTrunkId: id,
			Action:     &lkproto.UpdateSIPInboundTrunkRequest_Replace{Replace: want},
		})
		switch {
		case err == nil:
			n.InboundTrunkID = got.GetSipTrunkId()
			if n.InboundTrunkID == "" {
				n.InboundTrunkID = id
			}
			return nil
		case !isNotFound(err):
			return fmt.Errorf("livekit: update inbound trunk %s: %w", id, err)
		}
		// Stale ID: fall through and create a new trunk.
	}
	got, err := c.sip.CreateSIPInboundTrunk(ctx, &lkproto.CreateSIPInboundTrunkRequest{Trunk: want})
	if err != nil {
		return fmt.Errorf("livekit: create inbound trunk for %s: %w", n.Number, err)
	}
	n.InboundTrunkID = got.GetSipTrunkId()
	return nil
}

func (c *Client) ensureOutboundTrunk(ctx context.Context, n *domain.SIPNumber) error {
	want, err := buildOutboundTrunk(c.cfg, n)
	if err != nil {
		return err
	}
	id := n.OutboundTrunkID
	if id == "" {
		if id, err = c.findOutboundTrunk(ctx, n.Number); err != nil {
			return err
		}
	}
	if id != "" {
		got, err := c.sip.UpdateSIPOutboundTrunk(ctx, &lkproto.UpdateSIPOutboundTrunkRequest{
			SipTrunkId: id,
			Action:     &lkproto.UpdateSIPOutboundTrunkRequest_Replace{Replace: want},
		})
		switch {
		case err == nil:
			n.OutboundTrunkID = got.GetSipTrunkId()
			if n.OutboundTrunkID == "" {
				n.OutboundTrunkID = id
			}
			return nil
		case !isNotFound(err):
			return fmt.Errorf("livekit: update outbound trunk %s: %w", id, err)
		}
	}
	got, err := c.sip.CreateSIPOutboundTrunk(ctx, &lkproto.CreateSIPOutboundTrunkRequest{Trunk: want})
	if err != nil {
		return fmt.Errorf("livekit: create outbound trunk for %s: %w", n.Number, err)
	}
	n.OutboundTrunkID = got.GetSipTrunkId()
	return nil
}

func (c *Client) ensureDispatchRule(ctx context.Context, n *domain.SIPNumber) error {
	want, err := buildDispatchRule(c.cfg, n, n.InboundTrunkID)
	if err != nil {
		return err
	}
	id := n.DispatchRuleID
	if id == "" {
		if id, err = c.findDispatchRule(ctx, n.Number, n.InboundTrunkID); err != nil {
			return err
		}
	}
	if id != "" {
		got, err := c.sip.UpdateSIPDispatchRule(ctx, &lkproto.UpdateSIPDispatchRuleRequest{
			SipDispatchRuleId: id,
			Action:            &lkproto.UpdateSIPDispatchRuleRequest_Replace{Replace: want},
		})
		switch {
		case err == nil:
			n.DispatchRuleID = got.GetSipDispatchRuleId()
			if n.DispatchRuleID == "" {
				n.DispatchRuleID = id
			}
			return nil
		case !isNotFound(err):
			return fmt.Errorf("livekit: update dispatch rule %s: %w", id, err)
		}
	}
	got, err := c.sip.CreateSIPDispatchRule(ctx, &lkproto.CreateSIPDispatchRuleRequest{DispatchRule: want})
	if err != nil {
		return fmt.Errorf("livekit: create dispatch rule for %s: %w", n.Number, err)
	}
	n.DispatchRuleID = got.GetSipDispatchRuleId()
	return nil
}

func (c *Client) findInboundTrunk(ctx context.Context, number string) (string, error) {
	res, err := c.sip.ListSIPInboundTrunk(ctx, &lkproto.ListSIPInboundTrunkRequest{Numbers: []string{number}})
	if err != nil {
		return "", fmt.Errorf("livekit: list inbound trunks: %w", err)
	}
	name := inboundTrunkName(number)
	for _, t := range res.GetItems() {
		if t.GetName() == name {
			return t.GetSipTrunkId(), nil
		}
	}
	return "", nil
}

func (c *Client) findOutboundTrunk(ctx context.Context, number string) (string, error) {
	res, err := c.sip.ListSIPOutboundTrunk(ctx, &lkproto.ListSIPOutboundTrunkRequest{Numbers: []string{number}})
	if err != nil {
		return "", fmt.Errorf("livekit: list outbound trunks: %w", err)
	}
	name := outboundTrunkName(number)
	for _, t := range res.GetItems() {
		if t.GetName() == name {
			return t.GetSipTrunkId(), nil
		}
	}
	return "", nil
}

func (c *Client) findDispatchRule(ctx context.Context, number, trunkID string) (string, error) {
	req := &lkproto.ListSIPDispatchRuleRequest{}
	if trunkID != "" {
		req.TrunkIds = []string{trunkID}
	}
	res, err := c.sip.ListSIPDispatchRule(ctx, req)
	if err != nil {
		return "", fmt.Errorf("livekit: list dispatch rules: %w", err)
	}
	name := dispatchRuleName(number)
	for _, r := range res.GetItems() {
		if r.GetName() == name {
			return r.GetSipDispatchRuleId(), nil
		}
	}
	return "", nil
}

// removeInbound deletes the dispatch rule and inbound trunk of n.
func (c *Client) removeInbound(ctx context.Context, n *domain.SIPNumber) error {
	trunkID := n.InboundTrunkID
	if trunkID == "" {
		id, err := c.findInboundTrunk(ctx, n.Number)
		if err != nil {
			return err
		}
		trunkID = id
	}
	ruleID := n.DispatchRuleID
	if ruleID == "" {
		id, err := c.findDispatchRule(ctx, n.Number, trunkID)
		if err != nil {
			return err
		}
		ruleID = id
	}
	if ruleID != "" {
		if _, err := c.sip.DeleteSIPDispatchRule(ctx, &lkproto.DeleteSIPDispatchRuleRequest{SipDispatchRuleId: ruleID}); err != nil && !isNotFound(err) {
			return fmt.Errorf("livekit: delete dispatch rule %s: %w", ruleID, err)
		}
	}
	n.DispatchRuleID = ""
	if trunkID != "" {
		if _, err := c.sip.DeleteSIPTrunk(ctx, &lkproto.DeleteSIPTrunkRequest{SipTrunkId: trunkID}); err != nil && !isNotFound(err) {
			return fmt.Errorf("livekit: delete inbound trunk %s: %w", trunkID, err)
		}
	}
	n.InboundTrunkID = ""
	return nil
}

// removeOutbound deletes the outbound trunk of n.
func (c *Client) removeOutbound(ctx context.Context, n *domain.SIPNumber) error {
	trunkID := n.OutboundTrunkID
	if trunkID == "" {
		id, err := c.findOutboundTrunk(ctx, n.Number)
		if err != nil {
			return err
		}
		trunkID = id
	}
	if trunkID != "" {
		if _, err := c.sip.DeleteSIPTrunk(ctx, &lkproto.DeleteSIPTrunkRequest{SipTrunkId: trunkID}); err != nil && !isNotFound(err) {
			return fmt.Errorf("livekit: delete outbound trunk %s: %w", trunkID, err)
		}
	}
	n.OutboundTrunkID = ""
	return nil
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// Dial pre-creates the call room, dispatches the agent into it and places the
// outbound call with CreateSIPParticipant.
//
// Error semantics: a non-nil error means the call could not be placed
// (invalid request, LiveKit unreachable, auth, room/dispatch failure); the
// result's Error is then "failed: ...". Call outcomes (busy, no answer, SIP
// rejection) return a nil error with result.Error = "<status>: <detail>",
// where <status> is a domain.CallStatus (see DialStatus). The room is
// deleted on any failure.
func (c *Client) Dial(ctx context.Context, req domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	room := dialRoomName(c.cfg, req)
	req.RoomName = room
	md, err := marshalJSON(dialMetadata(req))
	if err != nil {
		return failed(err), err
	}
	sipReq, err := buildSIPParticipant(c.cfg, req, md)
	if err != nil {
		return failed(err), err
	}
	log := c.log.With().Str("room", room).Str("callId", req.CallID.String()).Str("to", sipReq.SipCallTo).Logger()

	if _, err := c.rooms.CreateRoom(ctx, buildCreateRoom(room, md)); err != nil {
		err = fmt.Errorf("livekit: create room %s: %w", room, err)
		return failed(err), err
	}
	if _, err := c.dispatch.CreateDispatch(ctx, buildAgentDispatch(c.cfg, room, md)); err != nil {
		c.cleanupRoom(ctx, room)
		err = fmt.Errorf("livekit: dispatch agent %q to %s: %w", c.cfg.AgentName, room, err)
		return failed(err), err
	}

	info, err := c.sip.CreateSIPParticipant(ctx, sipReq)
	if err != nil {
		c.cleanupRoom(ctx, room)
		if ctx.Err() != nil {
			err = fmt.Errorf("livekit: dial %s: %w", sipReq.SipCallTo, errors.Join(ctx.Err(), err))
			return failed(err), err
		}
		f := classifyDialError(err)
		if !f.Outcome {
			err = fmt.Errorf("livekit: dial %s: %w", sipReq.SipCallTo, err)
			return domain.OutboundCallResult{Error: f.Error()}, err
		}
		log.Info().Str("status", string(f.Status)).Int("sipCode", f.SIPCode).Msg("livekit: outbound call not connected")
		return domain.OutboundCallResult{Error: f.Error()}, nil
	}
	log.Info().Str("participantId", info.GetParticipantId()).Str("sipCallId", info.GetSipCallId()).
		Bool("answered", req.WaitUntilAnswered).Msg("livekit: outbound call placed")
	return domain.OutboundCallResult{
		ParticipantID: info.GetParticipantId(),
		SIPCallID:     info.GetSipCallId(),
		Answered:      req.WaitUntilAnswered,
	}, nil
}

func failed(err error) domain.OutboundCallResult {
	return domain.OutboundCallResult{Error: string(domain.StatusFailed) + ": " + err.Error()}
}

// cleanupRoom deletes a room after a failed dial, detached from ctx's
// cancellation so a cancelled request still cleans up.
func (c *Client) cleanupRoom(ctx context.Context, room string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := c.Hangup(cctx, room); err != nil {
		c.log.Warn().Err(err).Str("room", room).Msg("livekit: cleanup room after failed dial")
	}
}

// Hangup ends a call by deleting its room (which disconnects the SIP leg).
// A room that no longer exists is not an error.
func (c *Client) Hangup(ctx context.Context, roomName string) error {
	if roomName == "" {
		return fmt.Errorf("%w: missing room name", domain.ErrInvalid)
	}
	if _, err := c.rooms.DeleteRoom(ctx, &lkproto.DeleteRoomRequest{Room: roomName}); err != nil && !isNotFound(err) {
		return fmt.Errorf("livekit: delete room %s: %w", roomName, err)
	}
	return nil
}

// TransferCall SIP-REFERs the caller to toNumber ("tel:" URI unless a
// tel:/sip: URI is given). participantID may be the participant identity
// (e.g. "sip-+976..."), its SID ("PA_..."), or empty to pick the room's SIP
// participant.
func (c *Client) TransferCall(ctx context.Context, roomName, participantID, toNumber string) error {
	identity, err := c.resolveSIPIdentity(ctx, roomName, participantID)
	if err != nil {
		return err
	}
	req, err := buildTransfer(roomName, identity, toNumber)
	if err != nil {
		return err
	}
	if _, err := c.sip.TransferSIPParticipant(ctx, req); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("livekit: transfer %s in %s: %w", identity, roomName, domain.ErrNotFound)
		}
		return fmt.Errorf("livekit: transfer %s in %s to %s: %w", identity, roomName, req.TransferTo, err)
	}
	return nil
}

// resolveSIPIdentity maps a participant SID / identity / "" to an identity.
func (c *Client) resolveSIPIdentity(ctx context.Context, room, participant string) (string, error) {
	if room == "" {
		return "", fmt.Errorf("%w: missing room name", domain.ErrInvalid)
	}
	if participant != "" && !strings.HasPrefix(participant, "PA_") {
		return participant, nil
	}
	res, err := c.rooms.ListParticipants(ctx, &lkproto.ListParticipantsRequest{Room: room})
	if err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("livekit: room %s: %w", room, domain.ErrNotFound)
		}
		return "", fmt.Errorf("livekit: list participants of %s: %w", room, err)
	}
	for _, p := range res.GetParticipants() {
		if participant != "" {
			if p.GetSid() == participant {
				return p.GetIdentity(), nil
			}
			continue
		}
		if ParseSIPParticipant(p).IsSIP {
			return p.GetIdentity(), nil
		}
	}
	return "", fmt.Errorf("livekit: no SIP participant %q in room %s: %w", participant, room, domain.ErrNotFound)
}

// ListActiveRooms returns the names of LiveKit rooms that belong to CallGo
// calls (outbound "<prefix><uuid>" and inbound "<prefix>_..." rooms).
func (c *Client) ListActiveRooms(ctx context.Context) ([]string, error) {
	res, err := c.rooms.ListRooms(ctx, &lkproto.ListRoomsRequest{})
	if err != nil {
		return nil, fmt.Errorf("livekit: list rooms: %w", err)
	}
	inbound := c.cfg.inboundRoomPrefix() + "_"
	names := make([]string, 0, len(res.GetRooms()))
	for _, r := range res.GetRooms() {
		name := r.GetName()
		if strings.HasPrefix(name, c.cfg.RoomPrefix) || strings.HasPrefix(name, inbound) {
			names = append(names, name)
		}
	}
	return names, nil
}

// decodeMetadata decodes a JSON metadata string (room, participant or job).
// Invalid or empty input yields an empty map.
func decodeMetadata(s string) map[string]any {
	out := map[string]any{}
	if s == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]any{}
	}
	return out
}

// RoomMetadata decodes the JSON metadata of a room created by Dial (callId,
// direction, toNumber, fromNumber, ...). Useful in webhook handlers.
func RoomMetadata(r *lkproto.Room) map[string]any { return decodeMetadata(r.GetMetadata()) }
