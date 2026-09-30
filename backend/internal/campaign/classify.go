package campaign

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var (
	// SIP 486 Busy Here, 600 Busy Everywhere, 603 Decline and rejections.
	busyRe = regexp.MustCompile(`\bbusy\b|\b486\b|\b600\b|\b603\b|declin|reject`)
	// SIP 408 Request Timeout, 480 Temporarily Unavailable, 487 Request
	// Terminated (ring timeout), livekit-sip "sip request timed out".
	noAnswerRe = regexp.MustCompile(`no[ _-]?answer|not answered|timed out|timeout|deadline exceeded|\b408\b|\b480\b|\b487\b|temporarily unavailable`)
)

// classify maps an unanswered dial result to a terminal call status:
// busy, no_answer or failed.
func classify(res domain.OutboundCallResult, err error) domain.CallStatus {
	if err == nil && res.Error == "" {
		return domain.StatusNoAnswer
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.StatusNoAnswer
	}
	msg := strings.ToLower(res.Error)
	if err != nil {
		msg += " " + strings.ToLower(err.Error())
	}
	switch {
	case busyRe.MatchString(msg):
		return domain.StatusBusy
	case noAnswerRe.MatchString(msg):
		return domain.StatusNoAnswer
	default:
		return domain.StatusFailed
	}
}
