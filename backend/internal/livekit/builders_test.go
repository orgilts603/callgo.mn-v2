package livekit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func testConfig() Config {
	return Config{
		URL:                        "http://livekit.test",
		APIKey:                     "key",
		APISecret:                  "secret",
		SIPInboundAllowedAddresses: []string{"10.0.0.5/32"},
		SIPOutboundAddress:         "asterisk.local:5060",
		SIPOutboundTransport:       "TCP",
		SIPAuthUsername:            "lk",
		SIPAuthPassword:            "pw",
	}.withDefaults()
}

func testNumber() *domain.SIPNumber {
	return &domain.SIPNumber{
		ID:            uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		OrgID:         uuid.MustParse("99999999-8888-7777-6666-555555555555"),
		Number:        "+97677001234",
		Label:         "Main line",
		AllowInbound:  true,
		AllowOutbound: true,
		Active:        true,
	}
}

func TestConfigDefaultsAndValidate(t *testing.T) {
	cfg := Config{URL: "ws://x", APIKey: "k", APISecret: "s"}.withDefaults()
	assert.Equal(t, "callgo", cfg.AgentName)
	assert.Equal(t, "call-", cfg.RoomPrefix)
	assert.Equal(t, 30*time.Second, cfg.RingTimeout)
	assert.Equal(t, 30*time.Minute, cfg.MaxCallDuration)
	assert.Equal(t, "call-in", cfg.inboundRoomPrefix())
	require.NoError(t, cfg.validate())

	err := Config{}.withDefaults().validate()
	require.ErrorIs(t, err, domain.ErrInvalid)
	assert.Contains(t, err.Error(), "URL, APIKey, APISecret")

	bad := cfg
	bad.SIPOutboundTransport = "sctp"
	require.ErrorIs(t, bad.validate(), domain.ErrInvalid)

	bad = cfg
	bad.SIPAuthUsername = "only-user"
	require.ErrorIs(t, bad.validate(), domain.ErrInvalid)

	_, err = NewClient(Config{}, testLogger())
	require.ErrorIs(t, err, domain.ErrInvalid)
	c, err := NewClient(Config{URL: "ws://localhost:7880", APIKey: "k", APISecret: "s"}, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "call-"+uuid.Nil.String(), c.RoomName(uuid.Nil))
}

func TestParseTransport(t *testing.T) {
	for in, want := range map[string]lkproto.SIPTransport{
		"":     lkproto.SIPTransport_SIP_TRANSPORT_AUTO,
		"udp":  lkproto.SIPTransport_SIP_TRANSPORT_UDP,
		"TCP":  lkproto.SIPTransport_SIP_TRANSPORT_TCP,
		" tls": lkproto.SIPTransport_SIP_TRANSPORT_TLS,
	} {
		got, err := parseTransport(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

func TestBuildInboundTrunk(t *testing.T) {
	cfg := testConfig()
	cfg.SIPInboundAuthUsername, cfg.SIPInboundAuthPassword = "ast", "astpw"
	n := testNumber()
	tr, err := buildInboundTrunk(cfg, n)
	require.NoError(t, err)
	assert.Equal(t, "callgo-in-+97677001234", tr.Name)
	assert.Equal(t, []string{"+97677001234"}, tr.Numbers)
	assert.Equal(t, []string{"10.0.0.5/32"}, tr.AllowedAddresses)
	assert.Equal(t, "ast", tr.AuthUsername)
	assert.Equal(t, "astpw", tr.AuthPassword)
	assert.Empty(t, tr.SipTrunkId)
	assert.False(t, tr.KrispEnabled)

	var md map[string]string
	require.NoError(t, json.Unmarshal([]byte(tr.Metadata), &md))
	assert.Equal(t, n.ID.String(), md["sipNumberId"])
	assert.Equal(t, n.OrgID.String(), md["orgId"])

	// The config slice must not be aliased.
	tr.AllowedAddresses[0] = "changed"
	assert.Equal(t, "10.0.0.5/32", cfg.SIPInboundAllowedAddresses[0])

	_, err = buildInboundTrunk(cfg, &domain.SIPNumber{})
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = buildInboundTrunk(cfg, nil)
	require.ErrorIs(t, err, domain.ErrInvalid)
}

func TestBuildOutboundTrunk(t *testing.T) {
	cfg := testConfig()
	tr, err := buildOutboundTrunk(cfg, testNumber())
	require.NoError(t, err)
	assert.Equal(t, "callgo-out-+97677001234", tr.Name)
	assert.Equal(t, "asterisk.local:5060", tr.Address)
	assert.Equal(t, lkproto.SIPTransport_SIP_TRANSPORT_TCP, tr.Transport)
	assert.Equal(t, []string{"+97677001234"}, tr.Numbers)
	assert.Equal(t, "lk", tr.AuthUsername)
	assert.Equal(t, "pw", tr.AuthPassword)

	cfg.SIPOutboundAddress = ""
	_, err = buildOutboundTrunk(cfg, testNumber())
	require.ErrorIs(t, err, domain.ErrInvalid)
}

func TestBuildDispatchRule(t *testing.T) {
	cfg := testConfig()
	n := testNumber()
	r, err := buildDispatchRule(cfg, n, "ST_in")
	require.NoError(t, err)
	assert.Equal(t, "callgo-rule-+97677001234", r.Name)
	assert.Equal(t, []string{"ST_in"}, r.TrunkIds)

	ind := r.GetRule().GetDispatchRuleIndividual()
	require.NotNil(t, ind, "individual rule")
	assert.Equal(t, "call-in", ind.RoomPrefix)
	assert.False(t, ind.NoRandomness)

	require.NotNil(t, r.RoomConfig)
	require.Len(t, r.RoomConfig.Agents, 1)
	agent := r.RoomConfig.Agents[0]
	assert.Equal(t, "callgo", agent.AgentName)
	assert.JSONEq(t, `{"sipNumberId":"11111111-2222-3333-4444-555555555555","direction":"inbound"}`, agent.Metadata)
	assert.Equal(t, agent.Metadata, r.RoomConfig.Metadata)
	assert.Equal(t, n.ID.String(), r.Attributes[AttrSIPNumberID])
	assert.Equal(t, "inbound", r.Attributes[AttrDirection])
	require.NoError(t, r.Validate())

	_, err = buildDispatchRule(cfg, n, "")
	require.ErrorIs(t, err, domain.ErrInvalid)
}

func dialRequest() domain.OutboundCallRequest {
	n := testNumber()
	n.OutboundTrunkID = "ST_out"
	profileID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	return domain.OutboundCallRequest{
		CallID:     uuid.MustParse("12345678-1234-1234-1234-123456789abc"),
		FromNumber: *n,
		ToNumber:   "+97699112233",
		AgentProfile: domain.AgentProfile{
			ID:             profileID,
			MaxDurationSec: 600,
		},
		Metadata: map[string]any{
			"campaignId":  "c-1",
			"contactName": "Бат",
			"direction":   "spoofed", // must be overridden
		},
		WaitUntilAnswered: true,
		RingTimeout:       20 * time.Second,
	}
}

func TestDialMetadata(t *testing.T) {
	req := dialRequest()
	md := dialMetadata(req)
	assert.Equal(t, "outbound", md["direction"])
	assert.Equal(t, req.CallID.String(), md["callId"])
	assert.Equal(t, "+97699112233", md["toNumber"])
	assert.Equal(t, "+97677001234", md["fromNumber"])
	assert.Equal(t, req.FromNumber.ID.String(), md["sipNumberId"])
	assert.Equal(t, req.AgentProfile.ID.String(), md["agentProfileId"])
	assert.Equal(t, "c-1", md["campaignId"])
	// The caller's map is not mutated.
	assert.Equal(t, "spoofed", req.Metadata["direction"])
}

func TestBuildSIPParticipant(t *testing.T) {
	cfg := testConfig()
	req := dialRequest()
	p, err := buildSIPParticipant(cfg, req, `{"x":1}`)
	require.NoError(t, err)
	assert.Equal(t, "ST_out", p.SipTrunkId)
	assert.Equal(t, "+97699112233", p.SipCallTo)
	assert.Equal(t, "+97677001234", p.SipNumber)
	assert.Equal(t, "call-12345678-1234-1234-1234-123456789abc", p.RoomName)
	assert.Equal(t, "sip-+97699112233", p.ParticipantIdentity)
	assert.Equal(t, "Бат", p.ParticipantName)
	assert.Equal(t, `{"x":1}`, p.ParticipantMetadata)
	assert.True(t, p.WaitUntilAnswered)
	assert.Equal(t, 20*time.Second, p.RingingTimeout.AsDuration())
	assert.Equal(t, 10*time.Minute, p.MaxCallDuration.AsDuration())
	assert.False(t, p.KrispEnabled)
	assert.Equal(t, req.CallID.String(), p.ParticipantAttributes[AttrCallID])
	assert.Equal(t, "outbound", p.ParticipantAttributes[AttrDirection])

	// Defaults from config; explicit room name kept; name falls back to number.
	req.RingTimeout = 0
	req.AgentProfile.MaxDurationSec = 0
	req.RoomName = "custom-room"
	req.Metadata = nil
	p, err = buildSIPParticipant(cfg, req, "")
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, p.RingingTimeout.AsDuration())
	assert.Equal(t, 30*time.Minute, p.MaxCallDuration.AsDuration())
	assert.Equal(t, "custom-room", p.RoomName)
	assert.Equal(t, "+97699112233", p.ParticipantName)

	req.FromNumber.OutboundTrunkID = ""
	_, err = buildSIPParticipant(cfg, req, "")
	require.ErrorIs(t, err, domain.ErrInvalid)

	req = dialRequest()
	req.ToNumber = "  "
	_, err = buildSIPParticipant(cfg, req, "")
	require.ErrorIs(t, err, domain.ErrInvalid)
}

func TestBuildRoomAndDispatch(t *testing.T) {
	cfg := testConfig()
	r := buildCreateRoom("call-x", `{"a":1}`)
	assert.Equal(t, "call-x", r.Name)
	assert.Equal(t, `{"a":1}`, r.Metadata)
	assert.NotZero(t, r.EmptyTimeout)
	assert.NotZero(t, r.DepartureTimeout)

	d := buildAgentDispatch(cfg, "call-x", `{"a":1}`)
	assert.Equal(t, "callgo", d.AgentName)
	assert.Equal(t, "call-x", d.Room)
	assert.Equal(t, `{"a":1}`, d.Metadata)
}

func TestBuildTransfer(t *testing.T) {
	tr, err := buildTransfer("call-x", "sip-+976", "+97611112222")
	require.NoError(t, err)
	assert.Equal(t, "tel:+97611112222", tr.TransferTo)
	assert.True(t, tr.PlayDialtone)
	assert.Equal(t, "call-x", tr.RoomName)
	assert.Equal(t, "sip-+976", tr.ParticipantIdentity)

	tr, err = buildTransfer("call-x", "p", "sip:100@asterisk")
	require.NoError(t, err)
	assert.Equal(t, "sip:100@asterisk", tr.TransferTo)

	_, err = buildTransfer("call-x", "p", "")
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = buildTransfer("", "p", "+976")
	require.ErrorIs(t, err, domain.ErrInvalid)
}
