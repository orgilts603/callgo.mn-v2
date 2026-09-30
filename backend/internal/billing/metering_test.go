package billing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing/memory"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func usageByKind(t *testing.T, f *fixture, orgID uuid.UUID) map[domain.UsageKind]domain.UsageRecord {
	t.Helper()
	recs, _, err := f.store.ListUsage(f.ctx, orgID, t0.Add(-day), t0.Add(400*day), "", 0, 0)
	require.NoError(t, err)
	out := map[domain.UsageKind]domain.UsageRecord{}
	for _, r := range recs {
		_, dup := out[r.Kind]
		require.False(t, dup, "duplicate %s record", r.Kind)
		out[r.Kind] = r
	}
	return out
}

func TestRecordCallMetersAndCosts(t *testing.T) {
	f := newFixture(t)
	now := f.clock.Now()
	ans := now.Add(-2 * time.Minute)
	call := &domain.Call{
		ID: uuid.New(), OrgID: f.org.ID, StartedAt: ans, AnsweredAt: &ans, EndedAt: &now, DurationSec: 125,
		Usage: &domain.CallUsage{LLMTokensIn: 12_000, LLMTokensOut: 3_000, STTSeconds: 90, TTSChars: 2_500},
	}
	require.NoError(t, f.svc.RecordCall(f.ctx, call))
	require.NoError(t, f.svc.RecordCall(f.ctx, call), "second call is a no-op")

	u := usageByKind(t, f, f.org.ID)
	require.Len(t, u, 5)
	assert.Equal(t, float64(3), u[domain.UsageCallMinutes].Quantity) // 125s → 3 min
	assert.Equal(t, int64(60), u[domain.UsageCallMinutes].CostMNT)   // 3 × 20
	assert.Equal(t, float64(12_000), u[domain.UsageLLMTokensIn].Quantity)
	assert.Equal(t, int64(120), u[domain.UsageLLMTokensIn].CostMNT) // 12k × 10/1k
	assert.Equal(t, int64(30), u[domain.UsageLLMTokensOut].CostMNT)
	assert.Equal(t, int64(90), u[domain.UsageSTTSeconds].CostMNT) // 1.5 min × 60
	assert.Equal(t, int64(125), u[domain.UsageTTSChars].CostMNT)  // 2.5k × 50/1k
	require.NotNil(t, u[domain.UsageCallMinutes].CallID)
	assert.Equal(t, call.ID, *u[domain.UsageCallMinutes].CallID)
	assert.Equal(t, now, u[domain.UsageCallMinutes].At)

	ov, err := f.svc.Get(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, ov.Usage.Calls)
	assert.Equal(t, float64(3), ov.Usage.Minutes)
	assert.Equal(t, int64(15_000), ov.Usage.LLMTokens)
	assert.Equal(t, int64(60+120+30+90+125), ov.Usage.CostMNT)
}

func TestRecordCallUnansweredWithoutUsageRecordsNothing(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.RecordCall(f.ctx, &domain.Call{ID: uuid.New(), OrgID: f.org.ID, Status: domain.StatusNoAnswer}))
	assert.Empty(t, usageByKind(t, f, f.org.ID))

	err := f.svc.RecordCall(f.ctx, &domain.Call{ID: uuid.New()})
	assert.ErrorIs(t, err, domain.ErrInvalid)
	assert.ErrorIs(t, f.svc.RecordCall(f.ctx, nil), domain.ErrInvalid)
}

// repoWithoutChecker hides memory.Store.HasUsageForCall to exercise the
// ListUsage fallback.
type repoWithoutChecker struct{ domain.BillingRepository }

func TestRecordCallIdempotentWithoutUsageChecker(t *testing.T) {
	f := newFixture(t)
	svc := billing.New(repoWithoutChecker{f.store}, f.store, f.pay, f.bus, billing.Config{Now: f.clock.Now}, zeroLog())
	now := f.clock.Now()
	call := &domain.Call{ID: uuid.New(), OrgID: f.org.ID, StartedAt: now.Add(-time.Minute), AnsweredAt: &now, EndedAt: &now, DurationSec: 30}
	for range 3 {
		require.NoError(t, svc.RecordCall(f.ctx, call))
	}
	u := usageByKind(t, f, f.org.ID)
	assert.Equal(t, float64(1), u[domain.UsageCallMinutes].Quantity)
}

func TestRecordCallConcurrentIsIdempotent(t *testing.T) {
	f := newFixture(t)
	now := f.clock.Now()
	call := &domain.Call{ID: uuid.New(), OrgID: f.org.ID, AnsweredAt: &now, DurationSec: 61}
	done := make(chan error, 8)
	for range 8 {
		go func() { done <- f.svc.RecordCall(context.Background(), call) }()
	}
	for range 8 {
		require.NoError(t, <-done)
	}
	usageByKind(t, f, f.org.ID) // asserts no duplicates
}

func TestQuotaWarnings(t *testing.T) {
	f := newFixture(t) // trial: 100 included minutes
	f.callMinutes(f.org.ID, 79)
	assert.Empty(t, f.bus.ofType(billing.EventQuotaWarning))

	f.callMinutes(f.org.ID, 2) // 81 → crosses 80%
	ws := f.bus.ofType(billing.EventQuotaWarning)
	require.Len(t, ws, 1)
	p := ws[0].Payload.(map[string]any)
	assert.Equal(t, 80, p["percent"])
	assert.Equal(t, float64(81), p["used"])
	assert.Equal(t, 100, p["limit"])
	assert.Equal(t, f.org.ID, ws[0].OrgID)

	f.callMinutes(f.org.ID, 5) // 86: nothing new
	assert.Len(t, f.bus.ofType(billing.EventQuotaWarning), 1)

	f.callMinutes(f.org.ID, 20) // 106 → crosses 100%
	ws = f.bus.ofType(billing.EventQuotaWarning)
	require.Len(t, ws, 2)
	assert.Equal(t, 100, ws[1].Payload.(map[string]any)["percent"])

	g := newFixture(t)
	g.callMinutes(g.org.ID, 150) // jumps both thresholds → one warning at 100%
	ws = g.bus.ofType(billing.EventQuotaWarning)
	require.Len(t, ws, 1)
	assert.Equal(t, 100, ws[0].Payload.(map[string]any)["percent"])
}

func TestRecordSMS(t *testing.T) {
	f := newFixture(t)
	callID := uuid.New()
	require.NoError(t, f.svc.RecordSMS(f.ctx, f.org.ID, &callID))
	require.NoError(t, f.svc.RecordSMS(f.ctx, f.org.ID, nil))
	ov, err := f.svc.Get(f.ctx, f.org.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, ov.Usage.SMS)
	assert.Equal(t, int64(90), ov.Usage.CostMNT)
	assert.ErrorIs(t, f.svc.RecordSMS(f.ctx, uuid.Nil, nil), domain.ErrInvalid)
}

func TestUsageDailySeries(t *testing.T) {
	f := newFixture(t)
	f.callMinutes(f.org.ID, 3)
	f.callMinutes(f.org.ID, 2)
	f.clock.Advance(2 * day)
	f.callMinutes(f.org.ID, 10)

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	rep, err := f.svc.Usage(f.ctx, f.org.ID, from, to)
	require.NoError(t, err)
	assert.Equal(t, float64(15), rep.Summary.Minutes)
	assert.Equal(t, 3, rep.Summary.Calls)
	// Org timezone Asia/Ulaanbaatar (UTC+8): 2026-09-01 00:00Z is 08:00 local.
	require.Len(t, rep.Daily, 5)
	assert.Equal(t, "2026-09-01", rep.Daily[0].Day)
	assert.Equal(t, float64(5), rep.Daily[0].Minutes)
	assert.Equal(t, 2, rep.Daily[0].Calls)
	assert.Equal(t, int64(100), rep.Daily[0].CostMNT) // 5 min × 20
	assert.Equal(t, float64(0), rep.Daily[1].Minutes)
	assert.Equal(t, "2026-09-03", rep.Daily[2].Day)
	assert.Equal(t, float64(10), rep.Daily[2].Minutes)

	def, err := f.svc.Usage(f.ctx, f.org.ID, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, t0, def.Summary.PeriodStart, "defaults to the current period")
	assert.Len(t, def.Daily, 15)

	_, err = f.svc.Usage(f.ctx, f.org.ID, to, from)
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.Usage(f.ctx, f.org.ID, from, from.Add(500*day))
	assert.ErrorIs(t, err, domain.ErrInvalid)
}

func TestInvoiceHTML(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.ChangePlan(f.ctx, f.org.ID, billing.PlanStarter)
	require.NoError(t, err)
	org := f.orgStatus(f.org.ID)
	org.Name = `<script>alert("x")</script> ХХК`
	html := billing.InvoiceHTML(res.Invoice, &org)
	for _, want := range []string{
		"<!doctype html>", "НЭХЭМЖЛЭХ", res.Invoice.Number, "НӨАТ", "Нийт төлөх",
		"290 000 ₮", "29 000 ₮", "319 000 ₮", "Төлөгдөөгүй", "Starter багц", "ХХК",
	} {
		assert.Contains(t, html, want)
	}
	assert.NotContains(t, html, "<script>alert")

	paid, err := f.svc.MarkPaidManually(f.ctx, res.Invoice.ID, "")
	require.NoError(t, err)
	html = billing.InvoiceHTML(paid, nil)
	assert.Contains(t, html, "Төлөгдсөн")
	assert.Contains(t, html, "Төлсөн огноо")
	assert.True(t, strings.HasPrefix(html, "<!doctype html>"))
}

var _ billing.UsageChecker = (*memory.Store)(nil)
