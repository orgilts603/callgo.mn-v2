package campaign

import "github.com/orgilts603/callgo.mn-v2/backend/internal/domain"

// Call outcome accessors (plain field access; kept as helpers so call sites
// stay small).

func callOutcome(c *domain.Call) (code, note string) { return c.Outcome, c.OutcomeNote }

func setCallOutcome(c *domain.Call, code, note string) {
	c.Outcome, c.OutcomeNote = code, note
}
