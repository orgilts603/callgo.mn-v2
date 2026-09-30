package httpapi

import (
	"maps"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Call outcome accessors.
//
// docs/API.md gives every Call an `outcome` and `outcomeNote`. The frozen
// domain.Call may not declare Outcome/OutcomeNote yet; until it does, the
// values travel in Call.Metadata under "outcome" / "outcomeNote" — the same
// convention internal/crm uses to persist them in calls.outcome /
// calls.outcome_note. Once the fields exist these helpers use them directly.
const (
	metaOutcome     = "outcome"
	metaOutcomeNote = "outcomeNote"

	maxOutcomeCodeLen = 64
	maxOutcomeNoteLen = 1000
)

func callOutcomeFields(c *domain.Call) (code, note reflect.Value, ok bool) {
	v := reflect.ValueOf(c).Elem()
	code = v.FieldByName("Outcome")
	note = v.FieldByName("OutcomeNote")
	ok = code.IsValid() && code.Kind() == reflect.String && note.IsValid() && note.Kind() == reflect.String
	return code, note, ok
}

// getCallOutcome returns the AI-chosen outcome code and note of a call.
func getCallOutcome(c *domain.Call) (code, note string) {
	if f, n, ok := callOutcomeFields(c); ok {
		return f.String(), n.String()
	}
	code, _ = c.Metadata[metaOutcome].(string)
	note, _ = c.Metadata[metaOutcomeNote].(string)
	return code, note
}

// setCallOutcome stores the outcome on c. Metadata is cloned before it is
// changed because it may be shared with a cached copy.
func setCallOutcome(c *domain.Call, code, note string) {
	if f, n, ok := callOutcomeFields(c); ok {
		f.SetString(code)
		n.SetString(note)
		return
	}
	md := maps.Clone(c.Metadata)
	if md == nil {
		md = map[string]any{}
	}
	md[metaOutcome] = code
	md[metaOutcomeNote] = note
	c.Metadata = md
}

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
