// Package billing implements the CallGo.mn SaaS billing domain: the plan
// catalog, subscription lifecycle, usage metering, quota enforcement
// (domain.Entitlements), monthly invoicing, dunning and payment settlement
// through a domain.PaymentProvider (QPay or the mock).
package billing

import (
	"slices"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Plan codes.
const (
	PlanTrial      = "trial"
	PlanStarter    = "starter"
	PlanGrowth     = "growth"
	PlanEnterprise = "enterprise"
)

// Feature flags carried by domain.Plan.Features.
const (
	FeatureRecordings      = "recordings"
	FeatureWebhooks        = "webhooks"
	FeatureSMS             = "sms"
	FeatureAnalytics       = "analytics"
	FeatureAPI             = "api"
	FeatureHandoff         = "handoff"
	FeaturePrioritySupport = "priority_support"
)

// AllFeatures lists every feature flag in display order.
func AllFeatures() []string {
	return []string{FeatureRecordings, FeatureWebhooks, FeatureSMS, FeatureAnalytics, FeatureAPI, FeatureHandoff, FeaturePrioritySupport}
}

// Catalog is the static plan catalog (docs/ROADMAP_SAAS.md "Plans").
//
// Limit convention: for IncludedMinutes, MaxConcurrentCalls, MaxAgentProfiles,
// MaxUsers, MaxKnowledgeMB and MaxSIPNumbers a value <= 0 means unlimited
// (Unlimited reports this). OverageMNTPerMin == 0 means overage is blocked
// once the included minutes are used (irrelevant when minutes are unlimited).
type Catalog struct{}

// Plans is the plan catalog. It is stateless; every call returns fresh copies.
var Plans Catalog

// All returns every plan in display order.
func (Catalog) All() []domain.Plan {
	return []domain.Plan{
		{
			Code: PlanTrial, Name: "Туршилт (14 хоног)", MonthlyMNT: 0,
			IncludedMinutes: 100, OverageMNTPerMin: 0, MaxConcurrentCalls: 2,
			MaxAgentProfiles: 1, MaxUsers: 2, MaxKnowledgeMB: 10, MaxSIPNumbers: 1,
			Features: []string{}, TrialDays: 14, Public: false,
		},
		{
			Code: PlanStarter, Name: "Starter", MonthlyMNT: 290_000,
			IncludedMinutes: 1_000, OverageMNTPerMin: 350, MaxConcurrentCalls: 3,
			MaxAgentProfiles: 3, MaxUsers: 5, MaxKnowledgeMB: 50, MaxSIPNumbers: 2,
			Features: []string{FeatureRecordings, FeatureWebhooks, FeatureAnalytics}, Public: true,
		},
		{
			Code: PlanGrowth, Name: "Growth", MonthlyMNT: 890_000,
			IncludedMinutes: 4_000, OverageMNTPerMin: 300, MaxConcurrentCalls: 10,
			MaxAgentProfiles: 10, MaxUsers: 15, MaxKnowledgeMB: 500, MaxSIPNumbers: 5,
			Features: []string{
				FeatureRecordings, FeatureWebhooks, FeatureAnalytics,
				FeatureSMS, FeatureAPI, FeatureHandoff, FeaturePrioritySupport,
			},
			Public: true,
		},
		{
			// Custom deal: price 0 = "contact us"; limits come from
			// Subscription.CustomLimits when set, otherwise unlimited.
			Code: PlanEnterprise, Name: "Enterprise", MonthlyMNT: 0,
			IncludedMinutes: 0, OverageMNTPerMin: 0, MaxConcurrentCalls: 0,
			MaxAgentProfiles: 0, MaxUsers: 0, MaxKnowledgeMB: 0, MaxSIPNumbers: 0,
			Features: AllFeatures(), Public: true,
		},
	}
}

// Get returns the plan with the given code.
func (c Catalog) Get(code string) (domain.Plan, bool) {
	for _, p := range c.All() {
		if p.Code == code {
			return p, true
		}
	}
	return domain.Plan{}, false
}

// Public returns the plans shown on the pricing page (starter, growth and
// enterprise; the trial is assigned at signup and not selectable).
func (c Catalog) Public() []domain.Plan {
	var out []domain.Plan
	for _, p := range c.All() {
		if p.Public {
			out = append(out, p)
		}
	}
	return out
}

// SelfService reports whether an org may switch to code on its own
// (POST /api/billing/subscription). Trial and enterprise are assigned by
// signup / platform staff.
func (c Catalog) SelfService(code string) bool {
	p, ok := c.Get(code)
	return ok && p.Public && p.MonthlyMNT > 0
}

// Unlimited reports whether a cap value means "no limit".
func Unlimited(n int) bool { return n <= 0 }

// hasFeature reports whether p includes feature f.
func hasFeature(p domain.Plan, f string) bool { return slices.Contains(p.Features, f) }
