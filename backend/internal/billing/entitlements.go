package billing

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Reason prefixes returned by CanStartCall. The HTTP layer maps
// "quota_exceeded:" to 429 and "payment_required:" to 402.
const (
	ReasonQuotaExceeded   = "quota_exceeded"
	ReasonPaymentRequired = "payment_required"
)

// CanStartCall implements domain.Entitlements. It refuses when the org is
// not active, the subscription is not in good standing (trialing with the
// trial running, active, or past_due within the grace period for an org that
// has paid before), the concurrent-call cap is reached (the call being
// started is not yet counted), or the included minutes are used up on a plan
// without overage.
func (s *Service) CanStartCall(ctx context.Context, orgID uuid.UUID) (bool, string, error) {
	org, err := s.orgs.GetOrg(ctx, orgID)
	if err != nil {
		return false, "", fmt.Errorf("billing: get org: %w", err)
	}
	switch org.Status {
	case domain.OrgActive, "":
	case domain.OrgSuspended:
		return false, ReasonPaymentRequired + ": organization is suspended for non-payment", nil
	default:
		return false, ReasonPaymentRequired + ": organization is " + string(org.Status), nil
	}
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return false, "", err
	}
	now := s.now()
	switch sub.Status {
	case domain.SubTrialing:
		if sub.TrialEndsAt != nil && !now.Before(*sub.TrialEndsAt) {
			return false, ReasonPaymentRequired + ": trial has ended, choose a plan", nil
		}
	case domain.SubActive:
	case domain.SubPastDue:
		oldest, paidEver, err := s.openInvoices(ctx, orgID)
		if err != nil {
			return false, "", fmt.Errorf("billing: %w", err)
		}
		switch {
		case sub.PlanCode == PlanTrial:
			return false, ReasonPaymentRequired + ": trial has ended, choose a plan", nil
		case !paidEver:
			return false, ReasonPaymentRequired + ": pay the open invoice to activate the plan", nil
		case oldest != nil && !now.Before(oldest.DueAt.AddDate(0, 0, s.cfg.GraceDays)):
			return false, ReasonPaymentRequired + ": invoice " + oldest.Number + " is overdue", nil
		}
	case domain.SubCanceled:
		return false, ReasonPaymentRequired + ": subscription is canceled", nil
	default:
		return false, ReasonPaymentRequired + ": subscription is " + string(sub.Status), nil
	}

	limits := s.EffectiveLimits(sub)
	if !Unlimited(limits.MaxConcurrentCalls) {
		n, err := s.repo.CountActiveCalls(ctx, orgID)
		if err != nil {
			return false, "", fmt.Errorf("billing: count active calls: %w", err)
		}
		if n >= limits.MaxConcurrentCalls {
			return false, fmt.Sprintf("%s: concurrent call limit reached (%d)", ReasonQuotaExceeded, limits.MaxConcurrentCalls), nil
		}
	}
	if !Unlimited(limits.IncludedMinutes) && limits.OverageMNTPerMin == 0 {
		u, err := s.summarize(ctx, orgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd, limits)
		if err != nil {
			return false, "", err
		}
		if u.Minutes >= float64(limits.IncludedMinutes) {
			return false, ReasonQuotaExceeded + ": included minutes used", nil
		}
	}
	return true, "", nil
}

// HasFeature implements domain.Entitlements (false once canceled).
func (s *Service) HasFeature(ctx context.Context, orgID uuid.UUID, feature string) (bool, error) {
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return false, err
	}
	if sub.Status == domain.SubCanceled {
		return false, nil
	}
	return hasFeature(s.EffectiveLimits(sub), feature), nil
}

// Limits implements domain.Entitlements: the effective plan limits
// (CustomLimits when set). Caps <= 0 mean unlimited.
func (s *Service) Limits(ctx context.Context, orgID uuid.UUID) (domain.Plan, error) {
	sub, err := s.subscription(ctx, orgID)
	if err != nil {
		return domain.Plan{}, err
	}
	return s.EffectiveLimits(sub), nil
}
