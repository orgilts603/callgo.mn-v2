package billing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestCanStartCallMatrix(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fixture)
		ok     bool
		reason string
	}{
		{name: "fresh trial", setup: func(*fixture) {}, ok: true},
		{name: "trial concurrent cap", setup: func(f *fixture) { f.store.SetActiveCalls(f.org.ID, 2) }, reason: "quota_exceeded: concurrent call limit reached (2)"},
		{name: "trial below cap", setup: func(f *fixture) { f.store.SetActiveCalls(f.org.ID, 1) }, ok: true},
		{name: "trial minutes used", setup: func(f *fixture) { f.callMinutes(f.org.ID, 100) }, reason: "quota_exceeded: included minutes used"},
		{name: "trial expired before rollover", setup: func(f *fixture) {
			_, _ = f.svc.StartTrial(f.ctx, f.org.ID)
			f.clock.Advance(15 * day)
		}, reason: "payment_required: trial has ended"},
		{name: "trial expired after rollover", setup: func(f *fixture) {
			_, _ = f.svc.StartTrial(f.ctx, f.org.ID)
			f.clock.Advance(15 * day)
			_, _ = f.svc.RunPeriodRollover(f.ctx)
		}, reason: "payment_required: trial has ended"},
		{name: "starter overage allowed", setup: func(f *fixture) {
			f.paidSub(f.org.ID, billing.PlanStarter)
			f.callMinutes(f.org.ID, 1500)
		}, ok: true},
		{name: "starter concurrent cap", setup: func(f *fixture) {
			f.paidSub(f.org.ID, billing.PlanStarter)
			f.store.SetActiveCalls(f.org.ID, 3)
		}, reason: "quota_exceeded"},
		{name: "org suspended", setup: func(f *fixture) {
			o := f.orgStatus(f.org.ID)
			o.Status = domain.OrgSuspended
			f.store.PutOrg(o)
		}, reason: "payment_required: organization is suspended"},
		{name: "org closed", setup: func(f *fixture) {
			o := f.orgStatus(f.org.ID)
			o.Status = domain.OrgClosed
			f.store.PutOrg(o)
		}, reason: "payment_required: organization is closed"},
		{name: "enterprise unlimited", setup: func(f *fixture) {
			sub, _ := f.svc.StartTrial(f.ctx, f.org.ID)
			sub.PlanCode, sub.Status = billing.PlanEnterprise, domain.SubActive
			require.NoError(f.t, f.store.UpsertSubscription(f.ctx, sub))
			f.store.SetActiveCalls(f.org.ID, 500)
			f.callMinutes(f.org.ID, 100_000)
		}, ok: true},
		{name: "enterprise custom limits", setup: func(f *fixture) {
			sub, _ := f.svc.StartTrial(f.ctx, f.org.ID)
			sub.PlanCode, sub.Status = billing.PlanEnterprise, domain.SubActive
			sub.CustomLimits = &domain.Plan{MaxConcurrentCalls: 50, IncludedMinutes: 10_000}
			require.NoError(f.t, f.store.UpsertSubscription(f.ctx, sub))
			f.store.SetActiveCalls(f.org.ID, 50)
		}, reason: "quota_exceeded: concurrent call limit reached (50)"},
		{name: "past_due never paid", setup: func(f *fixture) {
			sub, _ := f.svc.StartTrial(f.ctx, f.org.ID)
			f.clock.Advance(15 * day)
			_, _ = f.svc.ChangePlan(f.ctx, sub.OrgID, billing.PlanStarter)
		}, reason: "payment_required: pay the open invoice"},
		{name: "past_due beyond grace", setup: func(f *fixture) {
			f.paidSub(f.org.ID, billing.PlanStarter)
			f.clock.Advance(31 * day)
			_, _ = f.svc.RunPeriodRollover(f.ctx)
			f.clock.Advance(15 * day)
			sub := f.sub(f.org.ID)
			sub.Status = domain.SubPastDue
			require.NoError(f.t, f.store.UpsertSubscription(f.ctx, sub))
		}, reason: "payment_required: invoice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			ok, reason, err := f.svc.CanStartCall(f.ctx, f.org.ID)
			require.NoError(t, err)
			assert.Equal(t, c.ok, ok, reason)
			if c.reason != "" {
				assert.Contains(t, reason, c.reason)
			} else {
				assert.Empty(t, reason)
			}
		})
	}
}

func TestHasFeatureAndLimits(t *testing.T) {
	f := newFixture(t)
	has, err := f.svc.HasFeature(f.ctx, f.org.ID, billing.FeatureRecordings)
	require.NoError(t, err)
	assert.False(t, has, "trial has no features")

	f.paidSub(f.org.ID, billing.PlanStarter)
	for feat, want := range map[string]bool{
		billing.FeatureRecordings: true, billing.FeatureWebhooks: true, billing.FeatureAnalytics: true,
		billing.FeatureSMS: false, billing.FeatureAPI: false, billing.FeatureHandoff: false,
	} {
		has, err := f.svc.HasFeature(f.ctx, f.org.ID, feat)
		require.NoError(t, err)
		assert.Equal(t, want, has, feat)
	}
	lim, err := f.svc.Limits(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, lim.MaxConcurrentCalls)

	sub := f.sub(f.org.ID)
	sub.PlanCode = billing.PlanEnterprise
	sub.CustomLimits = &domain.Plan{MaxUsers: 100, MonthlyMNT: 5_000_000}
	require.NoError(t, f.store.UpsertSubscription(f.ctx, sub))
	lim, err = f.svc.Limits(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, billing.PlanEnterprise, lim.Code)
	assert.Equal(t, 100, lim.MaxUsers)
	assert.ElementsMatch(t, billing.AllFeatures(), lim.Features, "features inherited from the plan")
	has, err = f.svc.HasFeature(f.ctx, f.org.ID, billing.FeatureHandoff)
	require.NoError(t, err)
	assert.True(t, has)

	// Enterprise with a custom monthly fee is invoiced at rollover.
	f.clock.Advance(32 * day)
	_, err = f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	invs, err := f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(5_500_000), invs[0].TotalMNT)
}
