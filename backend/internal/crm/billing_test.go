package crm

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestDefaultPlanLookup(t *testing.T) {
	p, ok := DefaultPlanLookup("starter")
	require.True(t, ok)
	require.Equal(t, int64(290_000), p.MonthlyMNT)
	require.Equal(t, 1000, p.IncludedMinutes)
	require.Equal(t, int64(350), p.OverageMNTPerMin)
	require.Equal(t, 3, p.MaxConcurrentCalls)

	p, ok = DefaultPlanLookup("trial")
	require.True(t, ok)
	require.Equal(t, 100, p.IncludedMinutes)
	require.Zero(t, p.OverageMNTPerMin, "trial overage is blocked")
	require.Equal(t, 14, p.TrialDays)

	p, ok = DefaultPlanLookup("growth")
	require.True(t, ok)
	require.Equal(t, 4000, p.IncludedMinutes)
	require.Equal(t, int64(300), p.OverageMNTPerMin)

	_, ok = DefaultPlanLookup("enterprise")
	require.True(t, ok)
	_, ok = DefaultPlanLookup("platinum")
	require.False(t, ok)
}

func TestSubscriptions(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "subs")
	org2 := newOrg(t, ctx, s, "subs-2")

	_, err := s.GetSubscription(ctx, org.ID)
	requireErrIs(t, err, domain.ErrNotFound)

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	trialEnd := start.Add(14 * 24 * time.Hour)
	sub := &domain.Subscription{OrgID: org.ID, PlanCode: "trial", CurrentPeriodStart: start,
		CurrentPeriodEnd: trialEnd, TrialEndsAt: &trialEnd}
	require.NoError(t, s.UpsertSubscription(ctx, sub))
	require.NotEqual(t, uuid.Nil, sub.ID)
	require.Equal(t, domain.SubTrialing, sub.Status, "status defaults to trialing")
	require.False(t, sub.CreatedAt.IsZero())
	require.Nil(t, sub.CustomLimits)
	firstID, created := sub.ID, sub.CreatedAt

	// Upsert replaces in place and mirrors the plan into the org.
	periodEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	custom := &domain.Plan{Code: "enterprise", IncludedMinutes: 50_000, OverageMNTPerMin: 200, MaxConcurrentCalls: 40}
	upd := &domain.Subscription{OrgID: org.ID, PlanCode: "enterprise", Status: domain.SubActive,
		CurrentPeriodStart: start, CurrentPeriodEnd: periodEnd, CustomLimits: custom}
	require.NoError(t, s.UpsertSubscription(ctx, upd))
	require.Equal(t, firstID, upd.ID)
	require.True(t, created.Equal(upd.CreatedAt))
	require.Nil(t, upd.TrialEndsAt)

	got, err := s.GetSubscription(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, "enterprise", got.PlanCode)
	require.Equal(t, domain.SubActive, got.Status)
	require.True(t, periodEnd.Equal(got.CurrentPeriodEnd))
	require.Equal(t, custom, got.CustomLimits)
	o, err := s.GetOrg(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, "enterprise", o.PlanCode)

	canceled := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, s.UpsertSubscription(ctx, &domain.Subscription{OrgID: org2.ID, PlanCode: "starter",
		Status: domain.SubCanceled, CurrentPeriodStart: start, CurrentPeriodEnd: trialEnd, CanceledAt: &canceled}))

	all, err := s.ListSubscriptions(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, org2.ID, all[0].OrgID, "soonest period end first")
	active, err := s.ListSubscriptions(ctx, domain.SubActive)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, org.ID, active[0].OrgID)
	cn, err := s.ListSubscriptions(ctx, domain.SubCanceled)
	require.NoError(t, err)
	require.True(t, canceled.Equal(*cn[0].CanceledAt))
	none, err := s.ListSubscriptions(ctx, domain.SubPastDue)
	require.NoError(t, err)
	require.NotNil(t, none)
	require.Empty(t, none)

	requireErrIs(t, s.UpsertSubscription(ctx, &domain.Subscription{OrgID: org.ID, PlanCode: "starter",
		Status: "limbo", CurrentPeriodStart: start, CurrentPeriodEnd: periodEnd}), domain.ErrInvalid)
	requireErrIs(t, s.UpsertSubscription(ctx, &domain.Subscription{OrgID: uuid.New(), PlanCode: "starter",
		CurrentPeriodStart: start, CurrentPeriodEnd: periodEnd}), domain.ErrInvalid)
	requireErrIs(t, s.UpsertSubscription(ctx, &domain.Subscription{OrgID: org.ID, PlanCode: "starter",
		CurrentPeriodStart: periodEnd, CurrentPeriodEnd: start}), domain.ErrInvalid)
	got, err = s.GetSubscription(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, "enterprise", got.PlanCode, "failed upserts roll back")
}

func TestUsageSummary(t *testing.T) {
	ctx, s := setup(t)
	t.Cleanup(func() { s.SetPlanLookup(nil) })
	s.SetPlanLookup(func(code string) (domain.Plan, bool) {
		if code == "test" {
			return domain.Plan{Code: "test", IncludedMinutes: 10, OverageMNTPerMin: 100}, true
		}
		return domain.Plan{}, false
	})

	org := newOrg(t, ctx, s, "usage")
	other := newOrg(t, ctx, s, "usage-other")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	require.NoError(t, s.UpsertSubscription(ctx, &domain.Subscription{OrgID: org.ID, PlanCode: "test",
		Status: domain.SubActive, CurrentPeriodStart: start, CurrentPeriodEnd: end}))

	c1 := newCall(t, ctx, s, domain.Call{OrgID: org.ID})
	c2 := newCall(t, ctx, s, domain.Call{OrgID: org.ID})
	in := start.Add(48 * time.Hour)
	recs := []domain.UsageRecord{
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageCallMinutes, Quantity: 6, CostMNT: 600, At: in},
		{OrgID: org.ID, CallID: &c2.ID, Kind: domain.UsageCallMinutes, Quantity: 3, CostMNT: 300, At: in},
		{OrgID: org.ID, CallID: &c2.ID, Kind: domain.UsageCallMinutes, Quantity: 2.5, CostMNT: 250, At: in.Add(time.Minute)},
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageLLMTokensIn, Quantity: 1200, CostMNT: 12, At: in},
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageLLMTokensOut, Quantity: 300, CostMNT: 9, At: in},
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageSTTSeconds, Quantity: 355.5, CostMNT: 30, At: in},
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageTTSChars, Quantity: 4200, CostMNT: 40, At: in},
		{OrgID: org.ID, Kind: domain.UsageSMS, Quantity: 1, CostMNT: 50, At: in},
		{OrgID: org.ID, Kind: domain.UsageSMS, Quantity: 2, CostMNT: 100, At: in},
		// Outside the period (end is exclusive) and another org: ignored.
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageCallMinutes, Quantity: 99, CostMNT: 1, At: end},
		{OrgID: org.ID, CallID: &c1.ID, Kind: domain.UsageCallMinutes, Quantity: 99, CostMNT: 1, At: start.Add(-time.Second)},
		{OrgID: other.ID, Kind: domain.UsageCallMinutes, Quantity: 77, CostMNT: 1, At: in},
	}
	require.NoError(t, s.AddUsage(ctx, recs))
	for _, r := range recs {
		require.NotEqual(t, uuid.Nil, r.ID, "IDs are written back")
	}
	require.NoError(t, s.AddUsage(ctx, nil))

	sum, err := s.SummarizeUsage(ctx, org.ID, start, end)
	require.NoError(t, err)
	require.Equal(t, org.ID, sum.OrgID)
	require.True(t, start.Equal(sum.PeriodStart))
	require.True(t, end.Equal(sum.PeriodEnd))
	require.Equal(t, 2, sum.Calls, "distinct calls with call_minutes")
	require.InDelta(t, 11.5, sum.Minutes, 1e-9)
	require.Equal(t, 10, sum.IncludedMinutes)
	require.InDelta(t, 1.5, sum.OverageMinutes, 1e-9)
	require.Equal(t, int64(200), sum.OverageMNT, "1.5 overage minutes bill as 2 started minutes × 100")
	require.Equal(t, int64(1500), sum.LLMTokens)
	require.InDelta(t, 355.5, sum.STTSeconds, 1e-9)
	require.Equal(t, int64(4200), sum.TTSChars)
	require.Equal(t, 3, sum.SMS)
	require.Equal(t, int64(600+300+250+12+9+30+40+50+100), sum.CostMNT)

	// Custom limits on the subscription override the plan.
	require.NoError(t, s.UpsertSubscription(ctx, &domain.Subscription{OrgID: org.ID, PlanCode: "test",
		Status: domain.SubActive, CurrentPeriodStart: start, CurrentPeriodEnd: end,
		CustomLimits: &domain.Plan{IncludedMinutes: 5, OverageMNTPerMin: 40}}))
	sum, err = s.SummarizeUsage(ctx, org.ID, start, end)
	require.NoError(t, err)
	require.Equal(t, 5, sum.IncludedMinutes)
	require.InDelta(t, 6.5, sum.OverageMinutes, 1e-9)
	require.Equal(t, int64(7*40), sum.OverageMNT)

	// Within the allowance: no overage.
	sum, err = s.SummarizeUsage(ctx, other.ID, start, end)
	require.NoError(t, err)
	require.Zero(t, sum.Calls, "records without a call are not calls")
	require.InDelta(t, 77, sum.Minutes, 1e-9)
	// other has no subscription: its org plan_code (trial) is unknown to the
	// test lookup → nothing included, overage not billable.
	require.Zero(t, sum.IncludedMinutes)
	require.InDelta(t, 77, sum.OverageMinutes, 1e-9)
	require.Zero(t, sum.OverageMNT)

	// Default catalog: trial includes 100 minutes, overage blocked.
	s.SetPlanLookup(nil)
	sum, err = s.SummarizeUsage(ctx, other.ID, start, end)
	require.NoError(t, err)
	require.Equal(t, 100, sum.IncludedMinutes)
	require.Zero(t, sum.OverageMinutes)
	require.Zero(t, sum.OverageMNT)

	empty, err := s.SummarizeUsage(ctx, org.ID, end.AddDate(0, 1, 0), end.AddDate(0, 2, 0))
	require.NoError(t, err)
	require.Zero(t, empty.Calls)
	require.Zero(t, empty.Minutes)
	require.Zero(t, empty.CostMNT)

	_, err = s.SummarizeUsage(ctx, uuid.New(), start, end)
	requireErrIs(t, err, domain.ErrNotFound)

	requireErrIs(t, s.AddUsage(ctx, []domain.UsageRecord{{OrgID: org.ID, Kind: "bitcoins", Quantity: 1}}), domain.ErrInvalid)
	requireErrIs(t, s.AddUsage(ctx, []domain.UsageRecord{{OrgID: uuid.New(), Kind: domain.UsageSMS, Quantity: 1}}),
		domain.ErrInvalid)
	requireErrIs(t, s.AddUsage(ctx, []domain.UsageRecord{{OrgID: org.ID, Kind: domain.UsageSMS, Quantity: -1}}),
		domain.ErrInvalid)
}

func TestListUsageAndActiveCalls(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "lu")
	start := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	recs := make([]domain.UsageRecord, 0, 6)
	for i := range 5 {
		recs = append(recs, domain.UsageRecord{OrgID: org.ID, Kind: domain.UsageCallMinutes, Quantity: float64(i + 1),
			At: start.Add(time.Duration(i) * time.Hour)})
	}
	recs = append(recs, domain.UsageRecord{OrgID: org.ID, Kind: domain.UsageSMS, Quantity: 1, At: start})
	require.NoError(t, s.AddUsage(ctx, recs))
	require.NoError(t, s.AddUsage(ctx, []domain.UsageRecord{{OrgID: org.ID, Kind: domain.UsageSMS, Quantity: 1}}))

	all, total, err := s.ListUsage(ctx, org.ID, start, end, "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, 6, total)
	require.Len(t, all, 6)

	page, total, err := s.ListUsage(ctx, org.ID, start, end, domain.UsageCallMinutes, 2, 1)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, page, 2)
	require.InDelta(t, 4, page[0].Quantity, 1e-9, "newest first")
	require.InDelta(t, 3, page[1].Quantity, 1e-9)
	require.Nil(t, page[0].CallID)

	now, total, err := s.ListUsage(ctx, org.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), domain.UsageSMS, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total, "zero At defaults to now")
	require.WithinDuration(t, time.Now(), now[0].At, time.Minute)

	// Concurrency: queued, ringing and active calls occupy a slot.
	for _, st := range []domain.CallStatus{domain.StatusQueued, domain.StatusRinging, domain.StatusActive,
		domain.StatusActive, domain.StatusCompleted, domain.StatusFailed, domain.StatusNoAnswer} {
		newCall(t, ctx, s, domain.Call{OrgID: org.ID, Status: st})
	}
	other := newOrg(t, ctx, s, "lu-other")
	newCall(t, ctx, s, domain.Call{OrgID: other.ID, Status: domain.StatusActive})
	n, err := s.CountActiveCalls(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	n, err = s.CountActiveCalls(ctx, uuid.New())
	require.NoError(t, err)
	require.Zero(t, n)
}

var invoiceNumberRe = regexp.MustCompile(`^CG-(\d{4})-(\d{6,})$`)

func invoiceSeq(t *testing.T, number string) (year string, seq int) {
	t.Helper()
	m := invoiceNumberRe.FindStringSubmatch(number)
	require.NotNilf(t, m, "invoice number %q", number)
	n, err := strconv.Atoi(m[2])
	require.NoError(t, err)
	return m[1], n
}

func TestInvoices(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "inv")
	other := newOrg(t, ctx, s, "inv-other")

	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	oct := sep.AddDate(0, 1, 0)
	inv := &domain.Invoice{OrgID: org.ID, PeriodStart: sep, PeriodEnd: oct,
		Lines: []domain.InvoiceLine{
			{Description: "Starter plan", Quantity: 1, UnitMNT: 290_000, AmountMNT: 290_000},
			{Description: "Overage minutes", Quantity: 20, UnitMNT: 350, AmountMNT: 7_000},
		},
		SubtotalMNT: 297_000, VATMNT: 29_700, TotalMNT: 326_700, Status: domain.InvoiceOpen,
		DueAt: oct.Add(7 * 24 * time.Hour)}
	require.NoError(t, s.CreateInvoice(ctx, inv))
	require.NotEqual(t, uuid.Nil, inv.ID)
	year, seq1 := invoiceSeq(t, inv.Number)
	require.Equal(t, "2026", year)
	require.False(t, inv.CreatedAt.IsZero())

	// Sequential numbers across orgs; draft status and DueAt = PeriodEnd defaults.
	inv2 := &domain.Invoice{OrgID: other.ID, PeriodStart: sep, PeriodEnd: oct}
	require.NoError(t, s.CreateInvoice(ctx, inv2))
	_, seq2 := invoiceSeq(t, inv2.Number)
	require.Equal(t, seq1+1, seq2)
	require.Equal(t, domain.InvoiceDraft, inv2.Status)
	require.True(t, oct.Equal(inv2.DueAt))
	require.Equal(t, []domain.InvoiceLine{}, inv2.Lines)

	// The year is taken in the org's timezone: Jan 1 00:00 in Ulaanbaatar is
	// still Dec 31 in UTC.
	ubNewYear := time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC)
	inv3 := &domain.Invoice{OrgID: org.ID, PeriodStart: ubNewYear, PeriodEnd: ubNewYear.AddDate(0, 1, 0)}
	require.NoError(t, s.CreateInvoice(ctx, inv3))
	year, seq3 := invoiceSeq(t, inv3.Number)
	require.Equal(t, "2027", year)
	require.Equal(t, seq2+1, seq3)
	require.Equal(t, fmt.Sprintf("CG-2027-%06d", seq3), inv3.Number)

	// One invoice per org and period.
	requireErrIs(t, s.CreateInvoice(ctx, &domain.Invoice{OrgID: org.ID, PeriodStart: sep, PeriodEnd: oct}),
		domain.ErrConflict)
	requireErrIs(t, s.CreateInvoice(ctx, &domain.Invoice{OrgID: uuid.New(), PeriodStart: sep, PeriodEnd: oct}),
		domain.ErrInvalid)
	requireErrIs(t, s.CreateInvoice(ctx, &domain.Invoice{OrgID: org.ID, PeriodStart: oct, PeriodEnd: oct,
		Status: "maybe"}), domain.ErrInvalid)

	got, err := s.GetInvoice(ctx, inv.ID)
	require.NoError(t, err)
	require.Equal(t, inv.Number, got.Number)
	require.Equal(t, inv.Lines, got.Lines)
	require.Equal(t, int64(326_700), got.TotalMNT)
	require.Equal(t, int64(29_700), got.VATMNT)
	require.Equal(t, int64(297_000), got.SubtotalMNT)
	require.Equal(t, domain.InvoiceOpen, got.Status)
	require.True(t, sep.Equal(got.PeriodStart))
	require.Nil(t, got.PaidAt)
	_, err = s.GetInvoice(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)

	paid := time.Now().UTC().Truncate(time.Microsecond)
	got.Status = domain.InvoicePaid
	got.PaidAt = &paid
	got.Number = "CG-HACK"
	require.NoError(t, s.UpdateInvoice(ctx, got))
	byPeriod, err := s.GetInvoiceForPeriod(ctx, org.ID, sep)
	require.NoError(t, err)
	require.Equal(t, inv.ID, byPeriod.ID)
	require.Equal(t, domain.InvoicePaid, byPeriod.Status)
	require.True(t, paid.Equal(*byPeriod.PaidAt))
	require.Equal(t, inv.Number, byPeriod.Number, "number is immutable")
	_, err = s.GetInvoiceForPeriod(ctx, org.ID, oct)
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.UpdateInvoice(ctx, &domain.Invoice{ID: uuid.New(), Status: domain.InvoiceVoid}), domain.ErrNotFound)

	list, err := s.ListInvoices(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, inv3.ID, list[0].ID, "newest period first")
	list, err = s.ListInvoices(ctx, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, list)
	require.Empty(t, list)
}

func TestPayments(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "pay")
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	inv := &domain.Invoice{OrgID: org.ID, PeriodStart: sep, PeriodEnd: sep.AddDate(0, 1, 0), TotalMNT: 326_700}
	require.NoError(t, s.CreateInvoice(ctx, inv))

	p1 := &domain.Payment{OrgID: org.ID, InvoiceID: inv.ID, Provider: "qpay", AmountMNT: 326_700}
	require.NoError(t, s.CreatePayment(ctx, p1))
	require.NotEqual(t, uuid.Nil, p1.ID)
	require.Equal(t, domain.PaymentPending, p1.Status)
	require.False(t, p1.CreatedAt.IsZero())
	// Several attempts may exist before the provider assigns a ref.
	p2 := &domain.Payment{OrgID: org.ID, InvoiceID: inv.ID, Provider: "qpay", AmountMNT: 326_700}
	require.NoError(t, s.CreatePayment(ctx, p2))

	got, err := s.GetPayment(ctx, p1.ID)
	require.NoError(t, err)
	require.Nil(t, got.DeepLinks)
	require.Nil(t, got.Raw)
	_, err = s.GetPaymentByProviderRef(ctx, "qpay", "")
	requireErrIs(t, err, domain.ErrNotFound)

	exp := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Microsecond)
	p1.ProviderRef = "qpay-inv-1"
	p1.QRText = "0002010102..."
	p1.QRImage = "iVBORw0KGgo="
	p1.DeepLinks = []domain.PaymentLink{{Name: "Khan bank", Logo: "https://x/khan.png", Link: "khanbank://q?qPay_QRcode=1"}}
	p1.ExpiresAt = &exp
	p1.Raw = map[string]any{"invoice_id": "qpay-inv-1"}
	require.NoError(t, s.UpdatePayment(ctx, p1))

	got, err = s.GetPaymentByProviderRef(ctx, "qpay", "qpay-inv-1")
	require.NoError(t, err)
	require.Equal(t, p1.ID, got.ID)
	require.Equal(t, p1.DeepLinks, got.DeepLinks)
	require.Equal(t, p1.Raw, got.Raw)
	require.Equal(t, "0002010102...", got.QRText)
	require.Equal(t, "iVBORw0KGgo=", got.QRImage)
	require.True(t, exp.Equal(*got.ExpiresAt))
	_, err = s.GetPaymentByProviderRef(ctx, "mock", "qpay-inv-1")
	requireErrIs(t, err, domain.ErrNotFound) // ref is scoped by provider

	// The same provider ref cannot back two payments.
	p2.ProviderRef = "qpay-inv-1"
	requireErrIs(t, s.UpdatePayment(ctx, p2), domain.ErrConflict)
	requireErrIs(t, s.CreatePayment(ctx, &domain.Payment{OrgID: org.ID, InvoiceID: inv.ID, Provider: "qpay",
		ProviderRef: "qpay-inv-1"}), domain.ErrConflict)
	require.NoError(t, s.CreatePayment(ctx, &domain.Payment{OrgID: org.ID, InvoiceID: inv.ID, Provider: "mock",
		ProviderRef: "qpay-inv-1"}))
	requireErrIs(t, s.CreatePayment(ctx, &domain.Payment{OrgID: org.ID, InvoiceID: uuid.New(), Provider: "qpay"}),
		domain.ErrInvalid)
	requireErrIs(t, s.CreatePayment(ctx, &domain.Payment{OrgID: org.ID, InvoiceID: inv.ID, Provider: "qpay",
		Status: "lost"}), domain.ErrInvalid)

	paidAt := time.Now().UTC().Truncate(time.Microsecond)
	got.Status = domain.PaymentPaid
	got.PaidAt = &paidAt
	require.NoError(t, s.UpdatePayment(ctx, got))
	got, err = s.GetPayment(ctx, p1.ID)
	require.NoError(t, err)
	require.Equal(t, domain.PaymentPaid, got.Status)
	require.True(t, paidAt.Equal(*got.PaidAt))
	require.Equal(t, "qpay-inv-1", got.ProviderRef)
	requireErrIs(t, s.UpdatePayment(ctx, &domain.Payment{ID: uuid.New(), Status: domain.PaymentFailed}), domain.ErrNotFound)
	_, err = s.GetPayment(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)

	list, err := s.ListPayments(ctx, inv.ID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, p1.ID, list[0].ID, "oldest first")
	list, err = s.ListPayments(ctx, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, list)
	require.Empty(t, list)
}
