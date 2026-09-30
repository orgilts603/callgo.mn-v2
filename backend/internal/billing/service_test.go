package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const day = 24 * time.Hour

func TestStartTrialIsIdempotent(t *testing.T) {
	f := newFixture(t)
	sub, err := f.svc.StartTrial(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, billing.PlanTrial, sub.PlanCode)
	assert.Equal(t, domain.SubTrialing, sub.Status)
	require.NotNil(t, sub.TrialEndsAt)
	assert.Equal(t, t0.Add(14*day), *sub.TrialEndsAt)
	assert.Equal(t, t0.Add(14*day), sub.CurrentPeriodEnd)
	assert.Equal(t, billing.PlanTrial, f.orgStatus(f.org.ID).PlanCode)

	again, err := f.svc.StartTrial(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, sub.ID, again.ID)
}

func TestGetStartsTrialForLegacyOrg(t *testing.T) {
	f := newFixture(t)
	ov, err := f.svc.Get(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, billing.PlanTrial, ov.Plan.Code)
	assert.Equal(t, 100, ov.Usage.IncludedMinutes)
	assert.Equal(t, 100, ov.Limits.IncludedMinutes)
}

func TestChangePlanDuringTrialActivatesImmediately(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.StartTrial(f.ctx, f.org.ID)
	require.NoError(t, err)
	f.clock.Advance(3 * day)

	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	now := f.clock.Now()
	assert.Equal(t, domain.SubActive, res.Subscription.Status)
	assert.Equal(t, billing.PlanStarter, res.Subscription.PlanCode)
	assert.Nil(t, res.Subscription.TrialEndsAt)
	assert.Equal(t, now, res.Subscription.CurrentPeriodStart)
	assert.Equal(t, time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC), res.Subscription.CurrentPeriodEnd)

	inv := res.Invoice
	require.NotNil(t, inv)
	assert.Equal(t, domain.InvoiceOpen, inv.Status)
	assert.Equal(t, "CG-2026-000001", inv.Number)
	require.Len(t, inv.Lines, 1)
	assert.Equal(t, int64(290_000), inv.Lines[0].AmountMNT)
	assert.Equal(t, int64(290_000), inv.SubtotalMNT)
	assert.Equal(t, int64(29_000), inv.VATMNT)
	assert.Equal(t, int64(319_000), inv.TotalMNT)
	assert.Equal(t, now.Add(7*day), inv.DueAt)
	assert.Equal(t, billing.PlanStarter, f.orgStatus(f.org.ID).PlanCode)
	assert.NotEmpty(t, f.bus.ofType(billing.EventBillingUpdated))

	ok, reason, err := f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.True(t, ok, reason)
}

func TestChangePlanAfterTrialExpiredStaysPastDueUntilPaid(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.StartTrial(f.ctx, f.org.ID)
	require.NoError(t, err)
	f.clock.Advance(15 * day)
	n, err := f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.SubPastDue, f.sub(f.org.ID).Status)
	assert.Equal(t, domain.OrgActive, f.orgStatus(f.org.ID).Status, "trial expiry leaves the org active")

	ok, reason, err := f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, reason, "payment_required")

	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanGrowth)
	require.NoError(t, err)
	assert.Equal(t, domain.SubPastDue, res.Subscription.Status)
	assert.Equal(t, int64(979_000), res.Invoice.TotalMNT)

	ok, reason, err = f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.False(t, ok, "plan is effective only when paid")
	assert.Contains(t, reason, "activate")

	// Pay through the mock provider.
	p, err := f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "mock")
	require.NoError(t, err)
	assert.Equal(t, "MOCK:"+p.ID.String(), p.QRText)
	assert.Equal(t, int64(979_000), p.AmountMNT)

	// Paying again reuses the pending payment.
	p2, err := f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "")
	require.NoError(t, err)
	assert.Equal(t, p.ID, p2.ID)

	got, err := f.svc.CheckPayment(f.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, got.Status)

	require.NoError(t, f.pay.MarkPaid(p.ProviderRef))
	got, err = f.svc.CheckPayment(f.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPaid, got.Status)
	require.NotNil(t, got.PaidAt)

	inv, pays, err := f.svc.Invoice(f.ctx, f.org.ID, res.Invoice.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	require.Len(t, pays, 1)
	assert.Equal(t, domain.SubActive, f.sub(f.org.ID).Status)

	ok, _, err = f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "mock")
	assert.ErrorIs(t, err, billing.ErrInvoiceNotOpen)
}

func TestChangePlanErrors(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanEnterprise)
	assert.ErrorIs(t, err, billing.ErrPlanUnavailable)
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanTrial)
	assert.ErrorIs(t, err, billing.ErrPlanUnavailable)
	_, err = f.svc.ChangePlan(f.ctx, f.org.ID, "nope")
	assert.ErrorIs(t, err, billing.ErrPlanUnavailable)

	f.paidSub(f.org.ID, billing.PlanStarter)
	_, err = f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	assert.ErrorIs(t, err, billing.ErrAlreadyOnPlan)
	assert.ErrorIs(t, err, domain.ErrConflict)
}

func TestUpgradeVoidsUnpaidInvoiceAndBillsOverage(t *testing.T) {
	f := newFixture(t)
	f.paidSub(f.org.ID, billing.PlanStarter)
	f.clock.Advance(10 * day)
	f.callMinutes(f.org.ID, 1010) // 10 minutes over the 1000 included
	f.clock.Advance(time.Minute)

	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanGrowth)
	require.NoError(t, err)
	assert.Equal(t, domain.SubActive, res.Subscription.Status)
	require.Len(t, res.Invoice.Lines, 2)
	assert.Equal(t, int64(890_000), res.Invoice.Lines[0].AmountMNT)
	assert.Equal(t, int64(3_500), res.Invoice.Lines[1].AmountMNT)
	assert.Equal(t, float64(10), res.Invoice.Lines[1].Quantity)
	assert.Equal(t, int64(893_500+89_350), res.Invoice.TotalMNT)

	// A cancel followed by choosing the current plan again undoes the cancel.
	_, err = f.svc.Cancel(f.ctx, f.org.ID)
	require.NoError(t, err)
	res2, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanGrowth)
	require.NoError(t, err)
	assert.Nil(t, res2.Subscription.CanceledAt)
	assert.Nil(t, res2.Invoice)
}

func TestUpgradeBeforePayingVoidsSupersededInvoice(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	f.clock.Advance(time.Hour)
	second, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanGrowth)
	require.NoError(t, err)
	assert.Equal(t, domain.SubActive, second.Subscription.Status)

	old, _, err := f.svc.Invoice(f.ctx, f.org.ID, first.Invoice.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.InvoiceVoid, old.Status)
	require.Len(t, second.Invoice.Lines, 1)
	assert.Equal(t, int64(979_000), second.Invoice.TotalMNT)
}

func TestDowngradeAppliesAtPeriodEnd(t *testing.T) {
	f := newFixture(t)
	f.paidSub(f.org.ID, billing.PlanGrowth)
	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	assert.Nil(t, res.Invoice)
	assert.Equal(t, billing.PlanStarter, res.PendingPlanCode)
	assert.Equal(t, billing.PlanGrowth, res.Subscription.PlanCode)
	ov, err := f.svc.Get(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, billing.PlanStarter, ov.PendingPlanCode)
	assert.Equal(t, billing.PlanGrowth, ov.Plan.Code)

	f.clock.Advance(31 * day)
	n, err := f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	sub := f.sub(f.org.ID)
	assert.Equal(t, billing.PlanStarter, sub.PlanCode)
	org := f.orgStatus(f.org.ID)
	assert.Equal(t, billing.PlanStarter, org.PlanCode)
	assert.NotContains(t, org.Settings, "billingPendingPlan")

	invs, err := f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	require.Len(t, invs, 2)
	assert.Equal(t, int64(319_000), invs[0].TotalMNT, "next period billed at the starter price")
	assert.Equal(t, sub.CurrentPeriodStart, invs[0].PeriodStart)
}

func TestCancelAtPeriodEnd(t *testing.T) {
	f := newFixture(t)
	f.paidSub(f.org.ID, billing.PlanStarter)
	sub, err := f.svc.Cancel(f.ctx, f.org.ID)
	require.NoError(t, err)
	require.NotNil(t, sub.CanceledAt)
	assert.Equal(t, domain.SubActive, sub.Status)
	ok, _, err := f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.True(t, ok, "active until the period ends")

	f.callMinutes(f.org.ID, 1002)
	f.clock.Advance(31 * day)
	_, err = f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, domain.SubCanceled, f.sub(f.org.ID).Status)
	invs, err := f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	require.Len(t, invs, 2)
	assert.Equal(t, int64(700), invs[0].SubtotalMNT, "final invoice bills only the overage")

	ok, reason, err := f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, reason, "canceled")
	has, err := f.svc.HasFeature(f.ctx, f.org.ID, billing.FeatureRecordings)
	require.NoError(t, err)
	assert.False(t, has)

	// Resubscribing starts a new period with an unpaid invoice.
	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	assert.Equal(t, domain.SubPastDue, res.Subscription.Status)
	require.NotNil(t, res.Invoice)
}

func TestCancelTrialIsImmediate(t *testing.T) {
	f := newFixture(t)
	sub, err := f.svc.Cancel(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.SubCanceled, sub.Status)
}

func TestRolloverInvoicesPlanFeeAndOverageIdempotently(t *testing.T) {
	f := newFixture(t)
	f.paidSub(f.org.ID, billing.PlanStarter)
	f.clock.Advance(5 * day)
	f.callMinutes(f.org.ID, 600)
	f.callMinutes(f.org.ID, 500) // 1100 total → 100 over

	f.clock.Advance(26 * day) // past 2026-10-01 10:00
	n, err := f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	sub := f.sub(f.org.ID)
	assert.Equal(t, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC), sub.CurrentPeriodStart)
	assert.Equal(t, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), sub.CurrentPeriodEnd)
	assert.Equal(t, domain.SubActive, sub.Status)

	invs, err := f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	require.Len(t, invs, 2)
	inv := invs[0]
	require.Len(t, inv.Lines, 2)
	assert.Equal(t, int64(290_000), inv.Lines[0].AmountMNT)
	assert.Equal(t, float64(100), inv.Lines[1].Quantity)
	assert.Equal(t, int64(350), inv.Lines[1].UnitMNT)
	assert.Equal(t, int64(35_000), inv.Lines[1].AmountMNT)
	assert.Equal(t, int64(325_000), inv.SubtotalMNT)
	assert.Equal(t, int64(32_500), inv.VATMNT)
	assert.Equal(t, int64(357_500), inv.TotalMNT)
	assert.Equal(t, sub.CurrentPeriodStart, inv.PeriodStart)

	n, err = f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	invs, err = f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Len(t, invs, 2, "no duplicate invoice")
	assert.NotEmpty(t, f.bus.ofType(billing.EventBillingUpdated))
}

func TestRolloverCatchesUpMissedPeriods(t *testing.T) {
	f := newFixture(t)
	f.paidSub(f.org.ID, billing.PlanStarter)
	f.clock.Advance(62 * day) // two periods missed
	_, err := f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	sub := f.sub(f.org.ID)
	assert.True(t, sub.CurrentPeriodEnd.After(f.clock.Now()))
	invs, err := f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Len(t, invs, 3)
}

func TestDunningSuspendsAndPaymentReactivates(t *testing.T) {
	f := newFixture(t)
	f.paidSub(f.org.ID, billing.PlanStarter)
	f.clock.Advance(31 * day)
	_, err := f.svc.RunPeriodRollover(f.ctx)
	require.NoError(t, err)
	invs, err := f.svc.Invoices(f.ctx, f.org.ID)
	require.NoError(t, err)
	open := invs[0]
	require.Equal(t, domain.InvoiceOpen, open.Status)

	n, err := f.svc.RunDunning(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "not due yet")

	f.clock.Advance(8 * day) // past due
	n, err = f.svc.RunDunning(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.SubPastDue, f.sub(f.org.ID).Status)
	assert.Equal(t, domain.OrgActive, f.orgStatus(f.org.ID).Status)
	ok, reason, err := f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.True(t, ok, "within grace: %s", reason)

	f.clock.Advance(7 * day) // due + 7d
	n, err = f.svc.RunDunning(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.OrgSuspended, f.orgStatus(f.org.ID).Status)
	ok, reason, err = f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, reason, "payment_required")

	n, err = f.svc.RunDunning(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "already suspended")

	// Payment via the callback path reactivates everything.
	p, err := f.svc.Pay(f.ctx, f.org.ID, open.ID, "mock")
	require.NoError(t, err)
	require.NoError(t, f.pay.MarkPaid(p.ID.String()))
	got, err := f.svc.HandleCallback(f.ctx, "mock", map[string]string{"payment_id": p.ID.String()}, nil)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPaid, got.Status)
	assert.Equal(t, domain.OrgActive, f.orgStatus(f.org.ID).Status)
	assert.Equal(t, domain.SubActive, f.sub(f.org.ID).Status)
	ok, _, err = f.svc.CanStartCall(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestHandleCallbackErrors(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.HandleCallback(f.ctx, "qpay", map[string]string{"payment_id": uuid.NewString()}, nil)
	assert.ErrorIs(t, err, billing.ErrUnknownProvider)
	_, err = f.svc.HandleCallback(f.ctx, "mock", map[string]string{}, nil)
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.HandleCallback(f.ctx, "mock", map[string]string{"payment_id": uuid.NewString()}, nil)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestPaymentForOrgLazyCheck(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.StartTrial(f.ctx, f.org.ID)
	require.NoError(t, err)
	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	p, err := f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "")
	require.NoError(t, err)
	require.NoError(t, f.pay.MarkPaid(p.ProviderRef))

	got, err := f.svc.PaymentForOrg(f.ctx, f.org.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, got.Status, "younger than CheckAfter: no provider call")

	f.clock.Advance(11 * time.Second)
	got, err = f.svc.PaymentForOrg(f.ctx, f.org.ID, p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPaid, got.Status)

	other := f.newOrg("Other")
	_, err = f.svc.PaymentForOrg(f.ctx, other.ID, p.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.Pay(f.ctx, other.ID, res.Invoice.ID, "mock")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "stripe")
	assert.ErrorIs(t, err, billing.ErrUnknownProvider)
}

func TestPaymentExpires(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	p, err := f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "mock")
	require.NoError(t, err)
	f.clock.Advance(25 * time.Hour)
	got, err := f.svc.CheckPayment(f.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentExpired, got.Status)
	p2, err := f.svc.Pay(f.ctx, f.org.ID, res.Invoice.ID, "mock")
	require.NoError(t, err)
	assert.NotEqual(t, p.ID, p2.ID, "expired payment is not reused")
}

func TestMarkPaidManually(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanGrowth)
	require.NoError(t, err)
	inv, err := f.svc.MarkPaidManually(f.ctx, res.Invoice.ID, "Хаан банк гүйлгээ #123")
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePaid, inv.Status)
	require.NotNil(t, inv.PaidAt)
	_, pays, err := f.svc.Invoice(f.ctx, f.org.ID, inv.ID)
	require.NoError(t, err)
	require.Len(t, pays, 1)
	assert.Equal(t, "bank_transfer", pays[0].Provider)
	assert.Equal(t, domain.PaymentPaid, pays[0].Status)

	again, err := f.svc.MarkPaidManually(f.ctx, inv.ID, "dup")
	require.NoError(t, err)
	assert.Equal(t, domain.InvoicePaid, again.Status)
	_, pays, err = f.svc.Invoice(f.ctx, f.org.ID, inv.ID)
	require.NoError(t, err)
	assert.Len(t, pays, 1, "idempotent")
}

func TestServiceWithoutBusOrProvider(t *testing.T) {
	f := newFixture(t)
	svc := billing.New(f.store, f.store, nil, nil, billing.Config{Now: f.clock.Now}, zeroLog())
	res, err := svc.ChangePlan(context.Background(), f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	_, err = svc.Pay(context.Background(), f.org.ID, res.Invoice.ID, "")
	assert.ErrorIs(t, err, billing.ErrUnknownProvider)
	assert.Empty(t, svc.Providers())
}
