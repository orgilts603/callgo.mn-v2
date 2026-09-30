package crm

import (
	"maps"
	"reflect"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Call outcome accessors.
//
// docs/API.md ("Campaign v2 additions") gives every Call an `outcome` and
// `outcomeNote`, persisted in calls.outcome / calls.outcome_note. The frozen
// domain.Call may not declare the Outcome/OutcomeNote fields yet (reported to
// the integrator). These helpers work in both states:
//
//   - when domain.Call has string fields Outcome and OutcomeNote they are used
//     directly;
//   - otherwise the values travel in Call.Metadata under "outcome" and
//     "outcomeNote": they are stored in the dedicated columns (not in the
//     metadata jsonb) and put back into Metadata when a call is read.
//
// Once the fields exist these helpers reduce to plain field access.
const (
	metaOutcome     = "outcome"
	metaOutcomeNote = "outcomeNote"
)

// callOutcomeFields returns the Outcome / OutcomeNote fields of c when
// domain.Call declares them.
func callOutcomeFields(c *domain.Call) (code, note reflect.Value, ok bool) {
	v := reflect.ValueOf(c).Elem()
	code = v.FieldByName("Outcome")
	note = v.FieldByName("OutcomeNote")
	ok = code.IsValid() && code.Kind() == reflect.String && note.IsValid() && note.Kind() == reflect.String
	return code, note, ok
}

// callOutcome returns the AI-chosen outcome code and note of a call.
func callOutcome(c *domain.Call) (code, note string) {
	if f, n, ok := callOutcomeFields(c); ok {
		return f.String(), n.String()
	}
	code, _ = c.Metadata[metaOutcome].(string)
	note, _ = c.Metadata[metaOutcomeNote].(string)
	return code, note
}

// setCallOutcome stores an outcome on a call read from the database.
func setCallOutcome(c *domain.Call, code, note string) {
	if f, n, ok := callOutcomeFields(c); ok {
		f.SetString(code)
		n.SetString(note)
		return
	}
	if code == "" && note == "" {
		return
	}
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	if code != "" {
		c.Metadata[metaOutcome] = code
	}
	if note != "" {
		c.Metadata[metaOutcomeNote] = note
	}
}

// callMetadataForStorage returns the metadata to persist in calls.metadata,
// without the outcome keys when they travel in Metadata.
func callMetadataForStorage(c *domain.Call) map[string]any {
	if _, _, ok := callOutcomeFields(c); ok {
		return c.Metadata
	}
	_, hasCode := c.Metadata[metaOutcome]
	_, hasNote := c.Metadata[metaOutcomeNote]
	if !hasCode && !hasNote {
		return c.Metadata
	}
	md := maps.Clone(c.Metadata)
	delete(md, metaOutcome)
	delete(md, metaOutcomeNote)
	return md
}
