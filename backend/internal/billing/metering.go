package billing

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// quota warning thresholds (percent of included minutes).
var quotaThresholds = [...]int{100, 80}

// BillableMinutes returns the billed minutes of a call: duration rounded up
// to whole minutes, at least 1 when the call was answered, 0 otherwise.
func BillableMinutes(call *domain.Call) int {
	sec := call.DurationSec
	if sec <= 0 && call.AnsweredAt != nil && call.EndedAt != nil {
		sec = int(call.EndedAt.Sub(*call.AnsweredAt).Seconds())
	}
	answered := call.AnsweredAt != nil || sec > 0
	if !answered {
		return 0
	}
	m := (sec + 59) / 60
	if m < 1 {
		m = 1
	}
	return m
}

func roundMNT(v float64) int64 { return int64(math.Round(v)) }

// callRecords builds the usage records of a finished call.
func (s *Service) callRecords(call *domain.Call, at time.Time) []domain.UsageRecord {
	c := s.cfg.ProviderCosts
	callID := call.ID
	var recs []domain.UsageRecord
	add := func(kind domain.UsageKind, qty float64, cost float64) {
		if qty <= 0 {
			return
		}
		recs = append(recs, domain.UsageRecord{
			ID: uuid.New(), OrgID: call.OrgID, CallID: &callID, Kind: kind,
			Quantity: qty, CostMNT: roundMNT(cost), At: at,
		})
	}
	mins := float64(BillableMinutes(call))
	add(domain.UsageCallMinutes, mins, mins*c.SIPPerMinMNT)
	if u := call.Usage; u != nil {
		add(domain.UsageLLMTokensIn, float64(u.LLMTokensIn), float64(u.LLMTokensIn)/1000*c.LLMPer1kTokensMNT)
		add(domain.UsageLLMTokensOut, float64(u.LLMTokensOut), float64(u.LLMTokensOut)/1000*c.LLMPer1kTokensMNT)
		add(domain.UsageSTTSeconds, u.STTSeconds, u.STTSeconds/60*c.STTPerMinMNT)
		add(domain.UsageTTSChars, float64(u.TTSChars), float64(u.TTSChars)/1000*c.TTSPer1kCharsMNT)
	}
	return recs
}

// RecordCall meters a finished call: call_minutes (rounded up, min 1 when
// answered) plus the LLM/STT/TTS usage the agent reported, with internal
// cost estimates from Config.ProviderCosts. It is idempotent per call ID.
// When the period's minutes cross 80% or 100% of the included allowance a
// quota.warning event is published.
func (s *Service) RecordCall(ctx context.Context, call *domain.Call) error {
	if call == nil || call.ID == uuid.Nil || call.OrgID == uuid.Nil {
		return fmt.Errorf("billing: record call: %w: call id and org id required", domain.ErrInvalid)
	}
	at := s.now()
	if call.EndedAt != nil {
		at = call.EndedAt.UTC()
	}
	recs := s.callRecords(call, at)
	if len(recs) == 0 {
		return nil
	}

	s.meterMu.Lock()
	defer s.meterMu.Unlock()
	done, err := s.hasUsageForCall(ctx, call, at)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	sub, err := s.subscription(ctx, call.OrgID)
	if err != nil {
		return err
	}
	limits := s.EffectiveLimits(sub)
	var before float64
	minutes := float64(BillableMinutes(call))
	checkQuota := minutes > 0 && !Unlimited(limits.IncludedMinutes)
	if checkQuota {
		u, err := s.summarize(ctx, call.OrgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, limits)
		if err != nil {
			return err
		}
		before = u.Minutes
	}
	if err := s.repo.AddUsage(ctx, recs); err != nil {
		return fmt.Errorf("billing: add usage: %w", err)
	}
	inPeriod := !at.Before(sub.CurrentPeriodStart) && at.Before(sub.CurrentPeriodEnd)
	if checkQuota && inPeriod {
		s.quotaWarning(ctx, call.OrgID, before, before+minutes, limits.IncludedMinutes)
	}
	return nil
}

// quotaWarning publishes quota.warning for the highest threshold crossed
// between before and after.
func (s *Service) quotaWarning(ctx context.Context, orgID uuid.UUID, before, after float64, included int) {
	for _, pct := range quotaThresholds {
		thr := float64(included) * float64(pct) / 100
		if before < thr && after >= thr {
			s.publish(ctx, orgID, EventQuotaWarning, map[string]any{"used": after, "limit": included, "percent": pct})
			s.log.Info().Str("orgId", orgID.String()).Int("percent", pct).Float64("used", after).Msg("quota warning")
			return
		}
	}
}

func (s *Service) hasUsageForCall(ctx context.Context, call *domain.Call, at time.Time) (bool, error) {
	if uc, ok := s.repo.(UsageChecker); ok {
		done, err := uc.HasUsageForCall(ctx, call.ID)
		if err != nil {
			return false, fmt.Errorf("billing: usage for call: %w", err)
		}
		return done, nil
	}
	// Fallback: scan the org's usage around the call's time window.
	from := call.StartedAt
	if from.IsZero() || from.After(at) {
		from = at.Add(-48 * time.Hour)
	}
	from = from.Add(-time.Hour)
	to := at.Add(time.Hour)
	if n := s.now().Add(time.Hour); n.After(to) {
		to = n
	}
	const page = 500
	for off := 0; ; off += page {
		recs, total, err := s.repo.ListUsage(ctx, call.OrgID, from, to, "", page, off)
		if err != nil {
			return false, fmt.Errorf("billing: list usage: %w", err)
		}
		for _, r := range recs {
			if r.CallID != nil && *r.CallID == call.ID {
				return true, nil
			}
		}
		if len(recs) < page || off+page >= total {
			return false, nil
		}
	}
}

// RecordSMS meters one outbound SMS (callID optional).
func (s *Service) RecordSMS(ctx context.Context, orgID uuid.UUID, callID *uuid.UUID) error {
	if orgID == uuid.Nil {
		return fmt.Errorf("billing: record sms: %w: org id required", domain.ErrInvalid)
	}
	rec := domain.UsageRecord{
		ID: uuid.New(), OrgID: orgID, CallID: callID, Kind: domain.UsageSMS, Quantity: 1,
		CostMNT: roundMNT(s.cfg.ProviderCosts.SMSPerMsgMNT), At: s.now(),
	}
	if err := s.repo.AddUsage(ctx, []domain.UsageRecord{rec}); err != nil {
		return fmt.Errorf("billing: add sms usage: %w", err)
	}
	return nil
}

// DailyUsage is one day of GET /api/billing/usage's series.
type DailyUsage struct {
	Day     string  `json:"day"` // YYYY-MM-DD in the org's timezone
	Minutes float64 `json:"minutes"`
	Calls   int     `json:"calls"`
	CostMNT int64   `json:"costMnt"`
}

// UsageReport is the answer of GET /api/billing/usage.
type UsageReport struct {
	Summary domain.UsageSummary `json:"summary"`
	Daily   []DailyUsage        `json:"daily"`
}

// maxUsageDays bounds the daily series.
const maxUsageDays = 400

// Usage summarises usage in [from, to) (zero values = current period) with a
// per-day series in the org's timezone (every day present, zeros included).
func (s *Service) Usage(ctx context.Context, orgID uuid.UUID, from, to time.Time) (*UsageReport, error) {
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if from.IsZero() {
		from = sub.CurrentPeriodStart
	}
	if to.IsZero() {
		to = sub.CurrentPeriodEnd
	}
	if !to.After(from) {
		return nil, fmt.Errorf("billing: usage: %w: 'to' must be after 'from'", domain.ErrInvalid)
	}
	if to.Sub(from) > maxUsageDays*24*time.Hour {
		return nil, fmt.Errorf("billing: usage: %w: range longer than %d days", domain.ErrInvalid, maxUsageDays)
	}
	sum, err := s.summarize(ctx, orgID, from, to, s.EffectiveLimits(sub))
	if err != nil {
		return nil, err
	}

	loc := time.UTC
	if org, err := s.orgs.GetOrg(ctx, orgID); err == nil && org.Timezone != "" {
		if l, err := time.LoadLocation(org.Timezone); err == nil {
			loc = l
		}
	}
	var days []DailyUsage
	idx := map[string]int{}
	for d := dayStart(from.In(loc)); d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format(dateLayout)
		idx[key] = len(days)
		days = append(days, DailyUsage{Day: key})
	}
	const page = 1000
	for off := 0; ; off += page {
		recs, total, err := s.repo.ListUsage(ctx, orgID, from, to, "", page, off)
		if err != nil {
			return nil, fmt.Errorf("billing: list usage: %w", err)
		}
		for _, r := range recs {
			i, ok := idx[r.At.In(loc).Format(dateLayout)]
			if !ok {
				continue
			}
			days[i].CostMNT += r.CostMNT
			if r.Kind == domain.UsageCallMinutes {
				days[i].Minutes += r.Quantity
				days[i].Calls++
			}
		}
		if len(recs) < page || off+page >= total {
			break
		}
	}
	if days == nil {
		days = []DailyUsage{}
	}
	return &UsageReport{Summary: sum, Daily: days}, nil
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
