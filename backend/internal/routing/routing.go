// Package routing decides who answers an inbound call on a SIP number:
// business hours, an after-hours profile or spoken message, a DTMF menu, or
// the number's default agent profile (docs/API.md "Inbound routing").
package routing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/campaign"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultTimezone is used when neither the business hours nor the
// organisation name a time zone.
const DefaultTimezone = campaign.DefaultTimezone

// Route modes reported in domain.ResolvedRoute.Mode.
const (
	ModeDirect     = "direct"
	ModeAfterHours = "after_hours"
	ModeMenu       = "menu"
)

// Menu limits and defaults.
const (
	MinMenuTimeoutSec     = 3
	MaxMenuTimeoutSec     = 30
	DefaultMenuTimeoutSec = 8
	MaxMenuRepeat         = 3
	DefaultMenuRepeat     = 1
)

const menuKeys = "0123456789*#"

// Location resolves an IANA time zone name. An empty name yields
// DefaultTimezone; an unknown name falls back to UTC.
func Location(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// hoursIn returns h with the time zone filled from tz when h has none.
func hoursIn(h domain.CampaignSchedule, tz *time.Location) domain.CampaignSchedule {
	if strings.TrimSpace(h.Timezone) == "" && tz != nil {
		h.Timezone = tz.String()
	}
	return h
}

// IsOpen reports whether the business hours are open at the given instant.
// Zero hours are always open. tz is the fallback zone for hours that carry
// none (normally the organisation's zone).
func IsOpen(hours domain.CampaignSchedule, at time.Time, tz *time.Location) bool {
	if hours.IsZero() {
		return true
	}
	return campaign.InWindow(hoursIn(hours, tz), at)
}

// NextOpen returns the first minute at or after from at which the hours are
// open. ok is false when they never open within the next eight days (an empty
// window such as start == end).
func NextOpen(hours domain.CampaignSchedule, from time.Time, tz *time.Location) (time.Time, bool) {
	if IsOpen(hours, from, tz) {
		return from, true
	}
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := from.Add(8 * 24 * time.Hour)
	for ; !t.After(limit); t = t.Add(time.Minute) {
		if IsOpen(hours, t, tz) {
			return t, true
		}
	}
	return time.Time{}, false
}

// Resolve applies n.Routing at the instant at, evaluating hours without their
// own time zone in tz (nil = DefaultTimezone). Order: closed hours →
// after_hours (profile or spoken message); non-empty menu → menu (with the
// number's profile as timeout fallback); else direct with n.AgentProfileID.
func Resolve(n domain.SIPNumber, at time.Time, tz *time.Location) domain.ResolvedRoute {
	if tz == nil {
		tz = Location("")
	}
	cfg := n.Routing
	if !IsOpen(cfg.BusinessHours, at, tz) {
		return domain.ResolvedRoute{
			Mode:           ModeAfterHours,
			AgentProfileID: cfg.AfterHoursProfile,
			Message:        cfg.AfterHoursMessage,
		}
	}
	if len(cfg.Menu) > 0 {
		timeout, repeat := cfg.MenuTimeoutSec, cfg.MenuRepeat
		if timeout <= 0 {
			timeout = DefaultMenuTimeoutSec
		}
		if repeat <= 0 {
			repeat = DefaultMenuRepeat
		}
		return domain.ResolvedRoute{
			Mode:           ModeMenu,
			AgentProfileID: n.AgentProfileID,
			MenuPrompt:     cfg.MenuPrompt,
			Menu:           slices.Clone(cfg.Menu),
			MenuTimeoutSec: timeout,
			MenuRepeat:     repeat,
		}
	}
	return domain.ResolvedRoute{Mode: ModeDirect, AgentProfileID: n.AgentProfileID}
}

// Normalize returns cfg with defaults applied: menu timeout and repeat when a
// menu exists, and trimmed keys/labels/messages.
func Normalize(cfg domain.RoutingConfig) domain.RoutingConfig {
	cfg.AfterHoursMessage = strings.TrimSpace(cfg.AfterHoursMessage)
	cfg.MenuPrompt = strings.TrimSpace(cfg.MenuPrompt)
	cfg.Menu = slices.Clone(cfg.Menu)
	for i := range cfg.Menu {
		cfg.Menu[i].Key = strings.TrimSpace(cfg.Menu[i].Key)
		cfg.Menu[i].Label = strings.TrimSpace(cfg.Menu[i].Label)
	}
	if len(cfg.Menu) > 0 {
		if cfg.MenuTimeoutSec == 0 {
			cfg.MenuTimeoutSec = DefaultMenuTimeoutSec
		}
		if cfg.MenuRepeat == 0 {
			cfg.MenuRepeat = DefaultMenuRepeat
		}
	}
	return cfg
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("routing: %s: %w", fmt.Sprintf(format, args...), domain.ErrInvalid)
}

// Validate checks cfg. profileExists reports whether a profile id belongs to
// the organisation. Errors wrap domain.ErrInvalid. Rules: business hours are
// a valid schedule; menu keys are unique single characters from 0-9, * and #;
// every menu option and the after-hours profile exist; menu timeout is 0
// (default 8) or 3..30; repeat is 0..3 (default 1); a menu needs a prompt;
// configured business hours need an after-hours profile or message.
func Validate(cfg domain.RoutingConfig, profileExists func(uuid.UUID) bool) error {
	if profileExists == nil {
		profileExists = func(uuid.UUID) bool { return true }
	}
	if err := campaign.ValidateSchedule(cfg.BusinessHours); err != nil {
		return fmt.Errorf("routing: business hours: %w", err)
	}
	if cfg.AfterHoursProfile != nil && !profileExists(*cfg.AfterHoursProfile) {
		return invalid("afterHoursProfileId does not exist")
	}
	if !cfg.BusinessHours.IsZero() && cfg.AfterHoursProfile == nil && strings.TrimSpace(cfg.AfterHoursMessage) == "" {
		return invalid("business hours need an afterHoursProfileId or afterHoursMessage")
	}
	if cfg.MenuTimeoutSec != 0 && (cfg.MenuTimeoutSec < MinMenuTimeoutSec || cfg.MenuTimeoutSec > MaxMenuTimeoutSec) {
		return invalid("menuTimeoutSec must be between %d and %d", MinMenuTimeoutSec, MaxMenuTimeoutSec)
	}
	if cfg.MenuRepeat < 0 || cfg.MenuRepeat > MaxMenuRepeat {
		return invalid("menuRepeat must be between 0 and %d", MaxMenuRepeat)
	}
	if len(cfg.Menu) == 0 {
		return nil
	}
	if strings.TrimSpace(cfg.MenuPrompt) == "" {
		return invalid("menuPrompt is required when a menu is configured")
	}
	seen := map[string]bool{}
	for i, o := range cfg.Menu {
		key := strings.TrimSpace(o.Key)
		if len(key) != 1 || !strings.Contains(menuKeys, key) {
			return invalid("menu[%d].key %q must be one of 0-9, * or #", i, o.Key)
		}
		if seen[key] {
			return invalid("menu key %q is used more than once", key)
		}
		seen[key] = true
		if !profileExists(o.AgentProfileID) {
			return invalid("menu[%d].agentProfileId does not exist", i)
		}
	}
	return nil
}

// ProfileLister is the slice of domain.AgentProfileRepository used to check
// that referenced profiles belong to an organisation.
type ProfileLister interface {
	ListAgentProfiles(ctx context.Context, orgID uuid.UUID) ([]domain.AgentProfile, error)
}

// ProfileSet loads the ids of the organisation's agent profiles for use in a
// Validate profileExists closure.
func ProfileSet(ctx context.Context, profiles ProfileLister, orgID uuid.UUID) (func(uuid.UUID) bool, error) {
	items, err := profiles.ListAgentProfiles(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list agent profiles: %w", err)
	}
	ids := make(map[uuid.UUID]struct{}, len(items))
	for _, p := range items {
		ids[p.ID] = struct{}{}
	}
	return func(id uuid.UUID) bool { _, ok := ids[id]; return ok }, nil
}

// ForNumber resolves the route of an already loaded number and organisation
// (org may be nil: the default zone is used).
func ForNumber(n *domain.SIPNumber, org *domain.Organization, now time.Time) (domain.ResolvedRoute, *uuid.UUID) {
	tzName := ""
	if org != nil {
		tzName = org.Timezone
	}
	route := Resolve(*n, now, Location(tzName))
	return route, route.AgentProfileID
}

// ForBootstrap resolves the inbound route for a dialed DID at now, for the
// agent bootstrap. It returns the route and the profile id to use (the route's
// AgentProfileID: the direct profile, the after-hours profile, or the menu
// fallback; nil when the after-hours route only speaks a message). The error
// wraps domain.ErrNotFound when the number is unknown.
func ForBootstrap(ctx context.Context, numbers domain.SIPNumberRepository, orgs domain.OrgRepository, number string, now time.Time) (domain.ResolvedRoute, *uuid.UUID, error) {
	n, err := numbers.GetSIPNumberByNumber(ctx, number)
	if err != nil {
		return domain.ResolvedRoute{}, nil, fmt.Errorf("get sip number %q: %w", number, err)
	}
	if n == nil {
		return domain.ResolvedRoute{}, nil, fmt.Errorf("sip number %q: %w", number, domain.ErrNotFound)
	}
	var org *domain.Organization
	if orgs != nil {
		o, err := orgs.GetOrg(ctx, n.OrgID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return domain.ResolvedRoute{}, nil, fmt.Errorf("get org: %w", err)
		}
		org = o
	}
	route, profileID := ForNumber(n, org, now)
	return route, profileID, nil
}
