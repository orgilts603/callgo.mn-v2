package livekit

import (
	"context"
	"errors"
	"fmt"
	"testing"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/xtwirp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// overTheWire simulates what the Go SDK hands back after a server-side error
// travelled through twirp (server → twirp metadata → client interceptor).
func overTheWire(err error) error {
	te := xtwirp.ToError(err)
	if st, ok := xtwirp.StatusFromError(te); ok {
		return st.Err()
	}
	return te
}

func sipErr(code lkproto.SIPStatusCode, text string) error {
	return (&lkproto.SIPStatus{Code: code, Status: text}).GRPCStatus().Err()
}

func TestClassifyDialError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  domain.CallStatus
		code    int
		outcome bool
	}{
		{"busy", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_BUSY_HERE, "Busy Here")), domain.StatusBusy, 486, true},
		{"decline", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_GLOBAL_DECLINE, "")), domain.StatusBusy, 603, true},
		{"unavailable", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_TEMPORARILY_UNAVAILABLE, "")), domain.StatusNoAnswer, 480, true},
		{"timeout", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_REQUEST_TIMEOUT, "")), domain.StatusNoAnswer, 408, true},
		{"not found", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_NOTFOUND, "Not Found")), domain.StatusFailed, 404, true},
		{"server error", overTheWire(sipErr(lkproto.SIPStatusCode_SIP_STATUS_SERVICE_UNAVAILABLE, "")), domain.StatusFailed, 503, true},
		{"twirp meta only", twirp.NewError(twirp.InvalidArgument, "x").WithMeta("sip_status_code", "486").WithMeta("sip_status", "Busy"), domain.StatusBusy, 486, true},
		{"rang out", twirp.NewError(twirp.Canceled, "sip request timed out"), domain.StatusNoAnswer, 0, true},
		{"deadline", fmt.Errorf("wrap: %w", context.DeadlineExceeded), domain.StatusNoAnswer, 0, true},
		{"unauthenticated", twirp.NewError(twirp.Unauthenticated, "bad token"), domain.StatusFailed, 0, false},
		{"network", errors.New("dial tcp: connection refused"), domain.StatusFailed, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := classifyDialError(tc.err)
			assert.Equal(t, tc.status, f.Status)
			assert.Equal(t, tc.code, f.SIPCode)
			assert.Equal(t, tc.outcome, f.Outcome)
			res := domain.OutboundCallResult{Error: f.Error()}
			assert.Equal(t, tc.status, DialStatus(res), res.Error)
		})
	}
	assert.Equal(t, "busy: sip 486 Busy Here", classifyDialError(overTheWire(sipErr(486, "Busy Here"))).Error())
	assert.Equal(t, "busy: sip 603 GLOBAL_DECLINE", classifyDialError(sipErr(603, "")).Error())
}

func TestIsNotFound(t *testing.T) {
	assert.True(t, isNotFound(twirp.NewError(twirp.NotFound, "requested room does not exist")))
	assert.True(t, isNotFound(fmt.Errorf("x: %w", twirp.NotFoundError("nope"))))
	assert.True(t, isNotFound(status.Error(codes.NotFound, "gone")))
	assert.False(t, isNotFound(twirp.NewError(twirp.Internal, "boom")))
	assert.False(t, isNotFound(errors.New("not found")))
	assert.False(t, isNotFound(nil))
}

func TestDialStatus(t *testing.T) {
	assert.Equal(t, domain.StatusActive, DialStatus(domain.OutboundCallResult{Answered: true}))
	assert.Equal(t, domain.StatusRinging, DialStatus(domain.OutboundCallResult{}))
	assert.Equal(t, domain.StatusNoAnswer, DialStatus(domain.OutboundCallResult{Error: "no_answer: simulated"}))
	assert.Equal(t, domain.StatusFailed, DialStatus(domain.OutboundCallResult{Error: "weird"}))
	require.Equal(t, domain.StatusBusy, DialStatus(domain.OutboundCallResult{Error: "busy"}))
}
