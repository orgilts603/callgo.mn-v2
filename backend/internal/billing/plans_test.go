package billing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
)

func TestPlansCatalog(t *testing.T) {
	trial, ok := billing.Plans.Get(billing.PlanTrial)
	require.True(t, ok)
	assert.Equal(t, 100, trial.IncludedMinutes)
	assert.Equal(t, 2, trial.MaxConcurrentCalls)
	assert.Equal(t, 1, trial.MaxAgentProfiles)
	assert.Equal(t, 2, trial.MaxUsers)
	assert.Equal(t, 10, trial.MaxKnowledgeMB)
	assert.Equal(t, 1, trial.MaxSIPNumbers)
	assert.Equal(t, 14, trial.TrialDays)
	assert.Zero(t, trial.OverageMNTPerMin)
	assert.Empty(t, trial.Features)
	assert.NotNil(t, trial.Features, "features must serialise as []")

	starter, _ := billing.Plans.Get(billing.PlanStarter)
	assert.Equal(t, int64(290_000), starter.MonthlyMNT)
	assert.Equal(t, 1000, starter.IncludedMinutes)
	assert.Equal(t, int64(350), starter.OverageMNTPerMin)
	assert.Equal(t, []int{3, 3, 5, 50, 2}, []int{starter.MaxConcurrentCalls, starter.MaxAgentProfiles, starter.MaxUsers, starter.MaxKnowledgeMB, starter.MaxSIPNumbers})
	assert.ElementsMatch(t, []string{"recordings", "webhooks", "analytics"}, starter.Features)

	growth, _ := billing.Plans.Get(billing.PlanGrowth)
	assert.Equal(t, int64(890_000), growth.MonthlyMNT)
	assert.Equal(t, 4000, growth.IncludedMinutes)
	assert.Equal(t, int64(300), growth.OverageMNTPerMin)
	assert.Equal(t, []int{10, 10, 15, 500, 5}, []int{growth.MaxConcurrentCalls, growth.MaxAgentProfiles, growth.MaxUsers, growth.MaxKnowledgeMB, growth.MaxSIPNumbers})
	assert.ElementsMatch(t, []string{"recordings", "webhooks", "analytics", "sms", "api", "handoff", "priority_support"}, growth.Features)

	ent, _ := billing.Plans.Get(billing.PlanEnterprise)
	assert.Zero(t, ent.MonthlyMNT)
	assert.True(t, billing.Unlimited(ent.IncludedMinutes))
	assert.True(t, billing.Unlimited(ent.MaxConcurrentCalls))
	assert.ElementsMatch(t, billing.AllFeatures(), ent.Features)

	_, ok = billing.Plans.Get("platinum")
	assert.False(t, ok)

	var codes []string
	for _, p := range billing.Plans.Public() {
		codes = append(codes, p.Code)
	}
	assert.Equal(t, []string{"starter", "growth", "enterprise"}, codes)

	assert.True(t, billing.Plans.SelfService("starter"))
	assert.True(t, billing.Plans.SelfService("growth"))
	assert.False(t, billing.Plans.SelfService("trial"))
	assert.False(t, billing.Plans.SelfService("enterprise"))

	// Returned copies are independent.
	a, _ := billing.Plans.Get(billing.PlanStarter)
	a.Features[0] = "hacked"
	b, _ := billing.Plans.Get(billing.PlanStarter)
	assert.Equal(t, "recordings", b.Features[0])
}

func TestVATAndMoney(t *testing.T) {
	assert.Equal(t, int64(29_000), billing.VAT(290_000, 10))
	assert.Equal(t, int64(3), billing.VAT(25, 10)) // 2.5 → 3
	assert.Equal(t, int64(2), billing.VAT(24, 10))
	assert.Equal(t, "319 000 ₮", billing.FormatMNT(319_000))
	assert.Equal(t, "1 234 567 ₮", billing.FormatMNT(1_234_567))
	assert.Equal(t, "0 ₮", billing.FormatMNT(0))
	assert.Equal(t, "-350 ₮", billing.FormatMNT(-350))
}
