// Package livekit implements domain.Telephony on top of LiveKit SIP.
//
// It owns three LiveKit SIP resources per domain.SIPNumber (an inbound trunk,
// an outbound trunk and an "individual room" dispatch rule that dispatches
// the CallGo agent), places outbound calls with CreateSIPParticipant, and
// offers helpers for LiveKit webhooks and room naming. A Mock implementation
// of domain.Telephony is provided for local development without a SIP trunk.
//
// The code is split into pure request builders (builders.go), error mapping
// (errors.go) and a thin client (client.go) that talks to LiveKit through
// small interfaces, so everything except the network hop is unit-testable.
package livekit

import (
	"fmt"
	"strings"
	"time"

	lkproto "github.com/livekit/protocol/livekit"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Defaults applied by Config.withDefaults.
const (
	DefaultAgentName       = "callgo"
	DefaultRoomPrefix      = "call-"
	DefaultRingTimeout     = 30 * time.Second
	DefaultMaxCallDuration = 30 * time.Minute

	// roomEmptyTimeout closes a pre-created outbound room if nobody ever
	// joins it (seconds).
	roomEmptyTimeout = 120
	// roomDepartureTimeout closes a room shortly after the last participant
	// left (seconds).
	roomDepartureTimeout = 10
)

// Config configures the LiveKit telephony adapter.
type Config struct {
	// URL of the LiveKit server (ws://, wss://, http:// or https://).
	URL       string
	APIKey    string
	APISecret string
	// AgentName is the agent worker name used for explicit dispatch
	// (default "callgo").
	AgentName string

	// SIPInboundAllowedAddresses restricts which IPs/CIDRs (Asterisk) may
	// send INVITEs to the inbound trunks.
	SIPInboundAllowedAddresses []string
	// SIPInboundAuthUsername / SIPInboundAuthPassword optionally require
	// digest auth from Asterisk on inbound trunks. Both empty = no auth
	// (rely on SIPInboundAllowedAddresses).
	SIPInboundAuthUsername string
	SIPInboundAuthPassword string

	// SIPOutboundAddress is the Asterisk host[:port] outbound calls are sent to.
	SIPOutboundAddress string
	// SIPOutboundTransport is "udp", "tcp", "tls" or "" (auto).
	SIPOutboundTransport string
	// SIPAuthUsername / SIPAuthPassword authenticate LiveKit towards Asterisk
	// on outbound calls.
	SIPAuthUsername string
	SIPAuthPassword string

	// RoomPrefix prefixes every call room (default "call-"). Outbound rooms
	// are RoomPrefix+<callID>; inbound rooms created by the dispatch rule are
	// RoomPrefix+"in_<caller>_<random>" (e.g. "call-in_+97699112233_Xy12").
	RoomPrefix string
	// RingTimeout is the default ring window for outbound calls (default 30s).
	RingTimeout time.Duration
	// MaxCallDuration caps a single outbound call (default 30m). An agent
	// profile's MaxDurationSec overrides it when set.
	MaxCallDuration time.Duration
}

// withDefaults returns a copy of c with zero values replaced by defaults.
func (c Config) withDefaults() Config {
	if c.AgentName == "" {
		c.AgentName = DefaultAgentName
	}
	if c.RoomPrefix == "" {
		c.RoomPrefix = DefaultRoomPrefix
	}
	if c.RingTimeout <= 0 {
		c.RingTimeout = DefaultRingTimeout
	}
	if c.MaxCallDuration <= 0 {
		c.MaxCallDuration = DefaultMaxCallDuration
	}
	c.SIPOutboundTransport = strings.ToLower(strings.TrimSpace(c.SIPOutboundTransport))
	return c
}

// validate checks the fields required to talk to LiveKit at all. Fields that
// are only needed for outbound provisioning are checked lazily.
func (c Config) validate() error {
	var missing []string
	if strings.TrimSpace(c.URL) == "" {
		missing = append(missing, "URL")
	}
	if c.APIKey == "" {
		missing = append(missing, "APIKey")
	}
	if c.APISecret == "" {
		missing = append(missing, "APISecret")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: livekit config: missing %s", domain.ErrInvalid, strings.Join(missing, ", "))
	}
	if _, err := parseTransport(c.SIPOutboundTransport); err != nil {
		return err
	}
	if (c.SIPInboundAuthUsername == "") != (c.SIPInboundAuthPassword == "") {
		return fmt.Errorf("%w: livekit config: inbound auth needs both username and password", domain.ErrInvalid)
	}
	if (c.SIPAuthUsername == "") != (c.SIPAuthPassword == "") {
		return fmt.Errorf("%w: livekit config: outbound auth needs both username and password", domain.ErrInvalid)
	}
	return nil
}

// inboundRoomPrefix is the prefix LiveKit's individual dispatch rule gets.
// LiveKit appends "_<caller>_<random>", so with the default prefix inbound
// rooms read "call-in_+97699112233_Xy12" — they still start with RoomPrefix,
// which is what webhook handlers and ListActiveRooms match on.
func (c Config) inboundRoomPrefix() string {
	return c.RoomPrefix + "in"
}

// parseTransport maps a config string to the protobuf enum.
func parseTransport(s string) (lkproto.SIPTransport, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return lkproto.SIPTransport_SIP_TRANSPORT_AUTO, nil
	case "udp":
		return lkproto.SIPTransport_SIP_TRANSPORT_UDP, nil
	case "tcp":
		return lkproto.SIPTransport_SIP_TRANSPORT_TCP, nil
	case "tls":
		return lkproto.SIPTransport_SIP_TRANSPORT_TLS, nil
	}
	return lkproto.SIPTransport_SIP_TRANSPORT_AUTO, fmt.Errorf("%w: livekit config: unknown SIP transport %q (want udp, tcp or tls)", domain.ErrInvalid, s)
}
