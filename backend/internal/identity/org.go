package identity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// reservedSettings are org.settings keys owned by other features (written
// through their own endpoints) and therefore not editable via PUT /api/org.
var reservedSettings = map[string]bool{"sms": true}

// OrgOverview is the GET /api/org response.
type OrgOverview struct {
	Org          *domain.Organization `json:"org"`
	Subscription *domain.Subscription `json:"subscription"`
	Plan         *domain.Plan         `json:"plan"`
}

// GetOrg returns the org with its subscription and effective plan limits.
func (s *Service) GetOrg(ctx context.Context, orgID uuid.UUID) (*OrgOverview, error) {
	org, err := s.getOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := &OrgOverview{Org: org, Subscription: s.subscription(ctx, orgID)}
	if s.Limits != nil {
		plan, err := s.Limits(ctx, orgID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.log.Warn().Err(err).Stringer("orgId", orgID).Msg("load plan limits")
		} else if err == nil {
			out.Plan = &plan
		}
	}
	return out, nil
}

// OrgUpdate is the PUT /api/org body (nil = unchanged). Settings are merged
// shallowly; a null value deletes the key.
type OrgUpdate struct {
	Name     *string        `json:"name,omitempty"`
	Timezone *string        `json:"timezone,omitempty"`
	Settings map[string]any `json:"settings,omitempty"`
}

// UpdateOrg edits the org's name, timezone and settings (owners/admins).
func (s *Service) UpdateOrg(ctx context.Context, actor Actor, upd OrgUpdate) (*domain.Organization, error) {
	if !actor.isAdmin() {
		return nil, errForbidden("only owners and admins can edit the organisation")
	}
	org, err := s.getOrg(ctx, actor.OrgID)
	if err != nil {
		return nil, err
	}
	if upd.Name != nil {
		name, err := validateName("name", *upd.Name, 120)
		if err != nil {
			return nil, err
		}
		org.Name = name
	}
	if upd.Timezone != nil {
		if *upd.Timezone == "" {
			return nil, errInvalid("timezone is required")
		}
		if _, err := time.LoadLocation(*upd.Timezone); err != nil {
			return nil, errInvalid("unknown timezone %q", *upd.Timezone)
		}
		org.Timezone = *upd.Timezone
	}
	if len(upd.Settings) > 0 {
		merged := maps.Clone(org.Settings)
		if merged == nil {
			merged = map[string]any{}
		}
		for k, v := range upd.Settings {
			if reservedSettings[k] {
				return nil, errInvalid("settings.%s is managed by its own endpoint", k)
			}
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		org.Settings = merged
	}
	org.UpdatedAt = s.now()
	if err := s.repo.UpdateOrg(ctx, org); err != nil {
		return nil, fmt.Errorf("update org: %w", err)
	}
	return org, nil
}

// Audit appends an audit entry. actor.Email is resolved from the user when
// empty; API-key actors are recorded as "api-key:<id>".
func (s *Service) Audit(ctx context.Context, orgID uuid.UUID, actor Actor, action, targetType, targetID string, meta map[string]any, ip string) error {
	e := &domain.AuditEntry{
		ID: uuid.New(), OrgID: orgID, ActorEmail: actor.Email, Action: action,
		TargetType: targetType, TargetID: targetID, Meta: meta, IP: ip, At: s.now(),
	}
	switch {
	case actor.APIKeyID != nil:
		e.ActorEmail = "api-key:" + actor.APIKeyID.String()
	case actor.UserID != uuid.Nil:
		id := actor.UserID
		e.ActorID = &id
		if e.ActorEmail == "" {
			if u, err := s.users.GetUser(ctx, id); err == nil {
				e.ActorEmail = u.Email
			}
		}
	}
	return s.Append(ctx, e)
}

// Append writes a prepared entry (fills ID and At). It lets the service act
// as the httpapi AuditWriter for other features. Failures are logged and
// returned.
func (s *Service) Append(ctx context.Context, e *domain.AuditEntry) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	if err := s.repo.AppendAudit(ctx, e); err != nil {
		s.log.Error().Err(err).Str("action", e.Action).Stringer("orgId", e.OrgID).Msg("append audit entry")
		return fmt.Errorf("append audit: %w", err)
	}
	return nil
}

// ListAudit pages the org's audit log (limit default 50, max 500).
func (s *Service) ListAudit(ctx context.Context, orgID uuid.UUID, f domain.AuditFilter) ([]domain.AuditEntry, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 500)
	f.Offset = max(f.Offset, 0)
	items, total, err := s.repo.ListAudit(ctx, orgID, f)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit: %w", err)
	}
	return items, total, nil
}
