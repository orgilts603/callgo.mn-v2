package campaign

import (
	"reflect"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Call outcome accessors.
//
// docs/API.md ("Campaign v2 additions") gives every Call an `outcome` and
// `outcomeNote`. The frozen domain.Call may not declare the Outcome /
// OutcomeNote fields yet (reported to the integrator). These helpers work in
// both states: they use the string fields Outcome / OutcomeNote when
// domain.Call has them, and otherwise Call.Metadata["outcome"] /
// Call.Metadata["outcomeNote"] (which internal/crm stores in the dedicated
// calls.outcome / calls.outcome_note columns). Once the fields exist these
// helpers reduce to plain field access.
const (
	metaOutcome     = "outcome"
	metaOutcomeNote = "outcomeNote"
)

func callOutcomeFields(c *domain.Call) (code, note reflect.Value, ok bool) {
	v := reflect.ValueOf(c).Elem()
	code = v.FieldByName("Outcome")
	note = v.FieldByName("OutcomeNote")
	ok = code.IsValid() && code.Kind() == reflect.String && note.IsValid() && note.Kind() == reflect.String
	return code, note, ok
}

// callOutcome returns the outcome code and note the AI chose for a call.
func callOutcome(c *domain.Call) (code, note string) {
	if f, n, ok := callOutcomeFields(c); ok {
		return f.String(), n.String()
	}
	code, _ = c.Metadata[metaOutcome].(string)
	note, _ = c.Metadata[metaOutcomeNote].(string)
	return code, note
}

// setCallOutcome stores an outcome on a call.
func setCallOutcome(c *domain.Call, code, note string) {
	if f, n, ok := callOutcomeFields(c); ok {
		f.SetString(code)
		n.SetString(note)
		return
	}
	if code == "" && note == "" {
		delete(c.Metadata, metaOutcome)
		delete(c.Metadata, metaOutcomeNote)
		return
	}
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	c.Metadata[metaOutcome] = code
	c.Metadata[metaOutcomeNote] = note
}
