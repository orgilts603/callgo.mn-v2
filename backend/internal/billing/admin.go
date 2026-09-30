package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// SetSubscription is the platform-admin override of an organisation's
// subscription (PUT /api/admin/orgs/{id}/subscription): it changes the plan
// and optionally the status, custom limits (enterprise deals) and the
// period end, keeps Organization.PlanCode in sync and publishes
// billing.updated. An org without a subscription gets one.
func (s *Service) SetSubscription(ctx context.Context, orgID uuid.UUID, planCode string,
	status *domain.SubscriptionStatus, custom *domain.Plan, periodEnd *time.Time) (*domain.Subscription, error) {
	if planCode == "" {
		return nil, fmt.Errorf("%w: planCode is required", domain.ErrInvalid)
	}
	if _, ok := Plans.Get(planCode); !ok {
		return nil, fmt.Errorf("%w: unknown plan %q", domain.ErrInvalid, planCode)
	}
	if status != nil {
		switch *status {
		case domain.SubTrialing, domain.SubActive, domain.SubPastDue, domain.SubCanceled:
		default:
			return nil, fmt.Errorf("%w: unknown subscription status %q", domain.ErrInvalid, *status)
		}
	}
	now := s.now()
	sub, err := s.getSub(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		sub = &domain.Subscription{
			ID: uuid.New(), OrgID: orgID, Status: domain.SubActive,
			CurrentPeriodStart: now, CurrentPeriodEnd: addMonth(now), CreatedAt: now,
		}
	}
	sub.PlanCode = planCode
	if status != nil {
		sub.Status = *status
		if *status == domain.SubCanceled {
			sub.CanceledAt = &now
		} else {
			sub.CanceledAt = nil
		}
	} else if sub.Status == domain.SubCanceled {
		sub.Status = domain.SubActive
		sub.CanceledAt = nil
	}
	if custom != nil {
		if custom.Code == "" && custom.Name == "" && custom.IncludedMinutes == 0 && custom.MaxConcurrentCalls == 0 &&
			custom.MaxAgentProfiles == 0 && custom.MaxUsers == 0 && custom.MaxSIPNumbers == 0 && len(custom.Features) == 0 {
			sub.CustomLimits = nil
		} else {
			c := *custom
			sub.CustomLimits = &c
		}
	}
	if periodEnd != nil {
		sub.CurrentPeriodEnd = periodEnd.UTC()
		if sub.Status == domain.SubTrialing {
			e := sub.CurrentPeriodEnd
			sub.TrialEndsAt = &e
		}
	}
	sub.UpdatedAt = now
	if err := s.repo.UpsertSubscription(ctx, sub); err != nil {
		return nil, fmt.Errorf("billing: set subscription: %w", err)
	}
	if err := s.syncOrg(ctx, orgID, func(o *domain.Organization) bool {
		changed := false
		if o.PlanCode != planCode {
			o.PlanCode, changed = planCode, true
		}
		if o.Settings != nil {
			if _, ok := o.Settings[pendingPlanKey]; ok {
				delete(o.Settings, pendingPlanKey)
				changed = true
			}
		}
		// A subscription made active again lifts a billing suspension.
		if o.Status == domain.OrgSuspended && (sub.Status == domain.SubActive || sub.Status == domain.SubTrialing) {
			o.Status, changed = domain.OrgActive, true
		}
		return changed
	}); err != nil {
		return nil, err
	}
	s.publishUpdated(ctx, sub)
	s.log.Info().Str("orgId", orgID.String()).Str("plan", planCode).Str("status", string(sub.Status)).Msg("subscription set by admin")
	return sub, nil
}
