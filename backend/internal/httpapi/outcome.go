package httpapi

import (
	"strings"
	"unicode/utf8"
)

// Bounds for the agent-supplied call outcome.
const (
	maxOutcomeCodeLen = 64
	maxOutcomeNoteLen = 1000
)

// cleanOutcome trims and bounds the agent-supplied outcome fields.
func cleanOutcome(code, note string) (string, string) {
	code = truncRunes(strings.ToLower(strings.TrimSpace(code)), maxOutcomeCodeLen)
	note = truncRunes(strings.Join(strings.Fields(note), " "), maxOutcomeNoteLen)
	return code, note
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
