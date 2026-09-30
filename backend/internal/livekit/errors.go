package livekit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// isNotFound reports whether a LiveKit API error means "does not exist".
// Errors arrive either as twirp errors or, when the server attached details,
// as gRPC status errors (xtwirp.ClientPassErrorDetails).
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var te twirp.Error
	if errors.As(err, &te) && te.Code() == twirp.NotFound {
		return true
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return true
	}
	return false
}

// sipStatusCode extracts the SIP response code of a failed call, if any.
func sipStatusCode(err error) (int, string) {
	if st := lkproto.SIPStatusFrom(err); st != nil && st.Code != 0 {
		return int(st.Code), st.Status
	}
	var te twirp.Error
	if errors.As(err, &te) {
		if v := te.Meta("sip_status_code"); v != "" {
			if code, convErr := strconv.Atoi(v); convErr == nil {
				return code, te.Meta("sip_status")
			}
		}
	}
	return 0, ""
}

// statusForSIPCode maps a final SIP response to a CallStatus.
func statusForSIPCode(code int) domain.CallStatus {
	switch code {
	case 486, 600, 603: // Busy Here, Busy Everywhere, Decline
		return domain.StatusBusy
	case 408, 480, 487: // Request Timeout, Temporarily Unavailable, Request Terminated
		return domain.StatusNoAnswer
	}
	return domain.StatusFailed
}

// isNoAnswer recognises the "rang out" error of livekit-sip, which has no SIP
// code: psrpc Canceled with "sip request timed out", or a deadline.
func isNoAnswer(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if strings.Contains(strings.ToLower(err.Error()), "sip request timed out") {
		return true
	}
	var te twirp.Error
	if errors.As(err, &te) && te.Code() == twirp.DeadlineExceeded {
		return true
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.DeadlineExceeded {
		return true
	}
	return false
}

// dialFailure is a classified CreateSIPParticipant error.
type dialFailure struct {
	Status domain.CallStatus
	// SIPCode is the SIP response code when the far end answered with one.
	SIPCode int
	// Outcome is true when the error is a call outcome (the callee was
	// reached or rang out) rather than an infrastructure failure.
	Outcome bool
	Detail  string
}

// Error renders the OutboundCallResult.Error string: "<status>: <detail>".
func (f dialFailure) Error() string {
	return string(f.Status) + ": " + f.Detail
}

// classifyDialError maps a CreateSIPParticipant error to a call outcome.
func classifyDialError(err error) dialFailure {
	if code, text := sipStatusCode(err); code > 0 {
		detail := fmt.Sprintf("sip %d", code)
		if text == "" {
			text = strings.TrimPrefix(lkproto.SIPStatusCode(code).String(), "SIP_STATUS_")
		}
		if text != "" && text != strconv.Itoa(code) {
			detail += " " + text
		}
		return dialFailure{Status: statusForSIPCode(code), SIPCode: code, Outcome: true, Detail: detail}
	}
	if isNoAnswer(err) {
		return dialFailure{Status: domain.StatusNoAnswer, Outcome: true, Detail: "no answer"}
	}
	return dialFailure{Status: domain.StatusFailed, Detail: err.Error()}
}

// DialStatus interprets an OutboundCallResult as a CallStatus:
// active when answered, the failure status encoded in Error ("busy",
// "no_answer", "failed", ...) when it failed, ringing otherwise (the call
// was placed without WaitUntilAnswered). Works for Client and Mock results.
func DialStatus(res domain.OutboundCallResult) domain.CallStatus {
	if res.Answered {
		return domain.StatusActive
	}
	if res.Error == "" {
		return domain.StatusRinging
	}
	prefix, _, _ := strings.Cut(res.Error, ":")
	switch s := domain.CallStatus(strings.TrimSpace(prefix)); s {
	case domain.StatusBusy, domain.StatusNoAnswer, domain.StatusFailed, domain.StatusVoicemail:
		return s
	}
	return domain.StatusFailed
}
