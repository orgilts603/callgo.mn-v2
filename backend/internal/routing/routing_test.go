package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var ub = Location("Asia/Ulaanbaatar") // UTC+8, no DST

// at builds an Ulaanbaatar-local instant. 2026-09-30 is a Wednesday.
func at(day, hour, min int) time.Time {
	return time.Date(2026, 9, day, hour, min, 0, 0, ub)
}

func weekdays(d ...time.Weekday) []time.Weekday { return d }

func TestResolve(t *testing.T) {
	defProfile := uuid.New()
	afterProfile := uuid.New()
	sales, support := uuid.New(), uuid.New()
	office := domain.CampaignSchedule{
		Weekdays:  weekdays(time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday),
		StartTime: "09:00", EndTime: "18:00",
	}
	night := domain.CampaignSchedule{StartTime: "22:00", EndTime: "06:00", Weekdays: weekdays(time.Friday)}
	menu := []domain.MenuOption{
		{Key: "1", Label: "Sales", AgentProfileID: sales},
		{Key: "2", Label: "Support", AgentProfileID: support},
	}

	tests := []struct {
		name    string
		routing domain.RoutingConfig
		when    time.Time
		want    domain.ResolvedRoute
	}{
		{
			name: "zero config is direct",
			when: at(30, 3, 0),
			want: domain.ResolvedRoute{Mode: ModeDirect, AgentProfileID: &defProfile},
		},
		{
			name:    "open hours weekday is direct",
			routing: domain.RoutingConfig{BusinessHours: office, AfterHoursMessage: "closed"},
			when:    at(30, 10, 0),
			want:    domain.ResolvedRoute{Mode: ModeDirect, AgentProfileID: &defProfile},
		},
		{
			name:    "closed before opening speaks message",
			routing: domain.RoutingConfig{BusinessHours: office, AfterHoursMessage: "closed"},
			when:    at(30, 8, 59),
			want:    domain.ResolvedRoute{Mode: ModeAfterHours, Message: "closed"},
		},
		{
			name:    "end is exclusive",
			routing: domain.RoutingConfig{BusinessHours: office, AfterHoursMessage: "closed"},
			when:    at(30, 18, 0),
			want:    domain.ResolvedRoute{Mode: ModeAfterHours, Message: "closed"},
		},
		{
			name:    "weekend is closed with after-hours profile",
			routing: domain.RoutingConfig{BusinessHours: office, AfterHoursProfile: &afterProfile, AfterHoursMessage: "closed"},
			when:    at(27, 12, 0), // Sunday
			want:    domain.ResolvedRoute{Mode: ModeAfterHours, AgentProfileID: &afterProfile, Message: "closed"},
		},
		{
			name:    "overnight open late evening",
			routing: domain.RoutingConfig{BusinessHours: night, AfterHoursMessage: "closed"},
			when:    at(25, 23, 30), // Friday
			want:    domain.ResolvedRoute{Mode: ModeDirect, AgentProfileID: &defProfile},
		},
		{
			name:    "overnight open after midnight belongs to previous day",
			routing: domain.RoutingConfig{BusinessHours: night, AfterHoursMessage: "closed"},
			when:    at(26, 2, 0), // Saturday 02:00, window opened Friday
			want:    domain.ResolvedRoute{Mode: ModeDirect, AgentProfileID: &defProfile},
		},
		{
			name:    "overnight closed midday",
			routing: domain.RoutingConfig{BusinessHours: night, AfterHoursMessage: "closed"},
			when:    at(25, 12, 0),
			want:    domain.ResolvedRoute{Mode: ModeAfterHours, Message: "closed"},
		},
		{
			name:    "overnight wrong weekday after midnight",
			routing: domain.RoutingConfig{BusinessHours: night, AfterHoursMessage: "closed"},
			when:    at(25, 2, 0), // Friday 02:00, window opened Thursday
			want:    domain.ResolvedRoute{Mode: ModeAfterHours, Message: "closed"},
		},
		{
			name:    "menu with defaults",
			routing: domain.RoutingConfig{MenuPrompt: "Press 1", Menu: menu},
			when:    at(30, 10, 0),
			want: domain.ResolvedRoute{Mode: ModeMenu, AgentProfileID: &defProfile, MenuPrompt: "Press 1", Menu: menu,
				MenuTimeoutSec: DefaultMenuTimeoutSec, MenuRepeat: DefaultMenuRepeat},
		},
		{
			name:    "menu with explicit timeout and repeat",
			routing: domain.RoutingConfig{MenuPrompt: "Press 1", Menu: menu, MenuTimeoutSec: 12, MenuRepeat: 3},
			when:    at(30, 10, 0),
			want: domain.ResolvedRoute{Mode: ModeMenu, AgentProfileID: &defProfile, MenuPrompt: "Press 1", Menu: menu,
				MenuTimeoutSec: 12, MenuRepeat: 3},
		},
		{
			name:    "closed hours beat the menu",
			routing: domain.RoutingConfig{BusinessHours: office, AfterHoursMessage: "closed", MenuPrompt: "Press 1", Menu: menu},
			when:    at(30, 20, 0),
			want:    domain.ResolvedRoute{Mode: ModeAfterHours, Message: "closed"},
		},
		{
			name:    "open hours with menu is menu",
			routing: domain.RoutingConfig{BusinessHours: office, AfterHoursMessage: "closed", MenuPrompt: "Press 1", Menu: menu},
			when:    at(30, 10, 0),
			want: domain.ResolvedRoute{Mode: ModeMenu, AgentProfileID: &defProfile, MenuPrompt: "Press 1", Menu: menu,
				MenuTimeoutSec: DefaultMenuTimeoutSec, MenuRepeat: DefaultMenuRepeat},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := domain.SIPNumber{AgentProfileID: &defProfile, Routing: tc.routing}
			got := Resolve(n, tc.when.UTC(), ub)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveUsesOrgTimezoneUnlessHoursHaveOwn(t *testing.T) {
	hours := domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"}
	n := domain.SIPNumber{Routing: domain.RoutingConfig{BusinessHours: hours, AfterHoursMessage: "closed"}}
	instant := time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC) // 10:00 in UB, 22:00 the previous day in New York

	assert.Equal(t, ModeDirect, Resolve(n, instant, ub).Mode)
	assert.Equal(t, ModeAfterHours, Resolve(n, instant, Location("America/New_York")).Mode)

	n.Routing.BusinessHours.Timezone = "America/New_York"
	assert.Equal(t, ModeAfterHours, Resolve(n, instant, ub).Mode, "explicit hours zone wins over the org zone")

	n.Routing.BusinessHours.Timezone = ""
	assert.Equal(t, ModeDirect, Resolve(n, instant, nil).Mode, "nil zone falls back to Asia/Ulaanbaatar")
}

func TestNextOpen(t *testing.T) {
	office := domain.CampaignSchedule{
		Weekdays:  weekdays(time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday),
		StartTime: "09:00", EndTime: "18:00",
	}
	got, ok := NextOpen(office, at(30, 12, 0).UTC(), ub)
	require.True(t, ok)
	assert.True(t, at(30, 12, 0).Equal(got), "already open returns from")

	got, ok = NextOpen(office, at(30, 18, 30).UTC(), ub)
	require.True(t, ok)
	assert.True(t, at(30, 18, 30).Before(got))
	assert.Equal(t, time.Date(2026, 10, 1, 9, 0, 0, 0, ub), got.In(ub), "next morning")

	got, ok = NextOpen(office, at(26, 12, 0).UTC(), ub) // Saturday
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 9, 28, 9, 0, 0, 0, ub), got.In(ub), "Monday")

	_, ok = NextOpen(domain.CampaignSchedule{StartTime: "09:00", EndTime: "09:00"}, at(30, 12, 0).UTC(), ub)
	assert.False(t, ok, "empty window never opens")

	got, ok = NextOpen(domain.CampaignSchedule{}, at(30, 12, 0).UTC(), ub)
	require.True(t, ok)
	assert.True(t, at(30, 12, 0).Equal(got))
}

func TestValidate(t *testing.T) {
	p1, p2, missing := uuid.New(), uuid.New(), uuid.New()
	exists := func(id uuid.UUID) bool { return id == p1 || id == p2 }
	menu := func(keys ...string) []domain.MenuOption {
		var out []domain.MenuOption
		for i, k := range keys {
			id := p1
			if i%2 == 1 {
				id = p2
			}
			out = append(out, domain.MenuOption{Key: k, Label: k, AgentProfileID: id})
		}
		return out
	}
	hours := domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"}

	tests := []struct {
		name string
		cfg  domain.RoutingConfig
		ok   bool
	}{
		{"zero", domain.RoutingConfig{}, true},
		{"hours with message", domain.RoutingConfig{BusinessHours: hours, AfterHoursMessage: "closed"}, true},
		{"hours with profile", domain.RoutingConfig{BusinessHours: hours, AfterHoursProfile: &p1}, true},
		{"hours without after-hours", domain.RoutingConfig{BusinessHours: hours}, false},
		{"hours with blank message", domain.RoutingConfig{BusinessHours: hours, AfterHoursMessage: "  "}, false},
		{"hours bad time", domain.RoutingConfig{BusinessHours: domain.CampaignSchedule{StartTime: "9am"}, AfterHoursMessage: "x"}, false},
		{"after-hours profile missing", domain.RoutingConfig{AfterHoursProfile: &missing}, false},
		{"menu ok", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1", "2", "0", "*", "#", "9")}, true},
		{"menu duplicate key", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1", "1")}, false},
		{"menu bad key letter", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("a")}, false},
		{"menu bad key two chars", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("10")}, false},
		{"menu empty key", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("")}, false},
		{"menu profile missing", domain.RoutingConfig{MenuPrompt: "p", Menu: []domain.MenuOption{{Key: "1", AgentProfileID: missing}}}, false},
		{"menu without prompt", domain.RoutingConfig{Menu: menu("1")}, false},
		{"timeout default", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuTimeoutSec: 0}, true},
		{"timeout min", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuTimeoutSec: 3}, true},
		{"timeout max", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuTimeoutSec: 30}, true},
		{"timeout too small", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuTimeoutSec: 2}, false},
		{"timeout too big", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuTimeoutSec: 31}, false},
		{"repeat max", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuRepeat: 3}, true},
		{"repeat too big", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuRepeat: 4}, false},
		{"repeat negative", domain.RoutingConfig{MenuPrompt: "p", Menu: menu("1"), MenuRepeat: -1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.cfg, exists)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalid), "want ErrInvalid, got %v", err)
		})
	}
	require.NoError(t, Validate(domain.RoutingConfig{AfterHoursProfile: &missing}, nil), "nil checker accepts any profile")
}

func TestNormalize(t *testing.T) {
	cfg := Normalize(domain.RoutingConfig{MenuPrompt: " hi ", Menu: []domain.MenuOption{{Key: " 1 ", Label: " a "}}})
	assert.Equal(t, DefaultMenuTimeoutSec, cfg.MenuTimeoutSec)
	assert.Equal(t, DefaultMenuRepeat, cfg.MenuRepeat)
	assert.Equal(t, "hi", cfg.MenuPrompt)
	assert.Equal(t, "1", cfg.Menu[0].Key)
	assert.Equal(t, "a", cfg.Menu[0].Label)

	none := Normalize(domain.RoutingConfig{})
	assert.Zero(t, none.MenuTimeoutSec, "no menu, no defaults")
	assert.Zero(t, none.MenuRepeat)
}

type stubNumbers struct {
	domain.SIPNumberRepository
	n   *domain.SIPNumber
	err error
}

func (s stubNumbers) GetSIPNumberByNumber(context.Context, string) (*domain.SIPNumber, error) {
	return s.n, s.err
}

type stubOrgs struct {
	domain.OrgRepository
	o *domain.Organization
}

func (s stubOrgs) GetOrg(context.Context, uuid.UUID) (*domain.Organization, error) {
	if s.o == nil {
		return nil, domain.ErrNotFound
	}
	return s.o, nil
}

func TestForBootstrap(t *testing.T) {
	ctx := context.Background()
	prof := uuid.New()
	n := &domain.SIPNumber{
		ID: uuid.New(), OrgID: uuid.New(), Number: "+97677001234", AgentProfileID: &prof,
		Routing: domain.RoutingConfig{
			BusinessHours:     domain.CampaignSchedule{StartTime: "09:00", EndTime: "18:00"},
			AfterHoursMessage: "closed",
		},
	}
	open := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC) // 11:00 UB
	shut := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)

	route, id, err := ForBootstrap(ctx, stubNumbers{n: n}, stubOrgs{o: &domain.Organization{Timezone: "Asia/Ulaanbaatar"}}, n.Number, open)
	require.NoError(t, err)
	assert.Equal(t, ModeDirect, route.Mode)
	require.NotNil(t, id)
	assert.Equal(t, prof, *id)

	route, id, err = ForBootstrap(ctx, stubNumbers{n: n}, stubOrgs{}, n.Number, shut) // org missing: default zone
	require.NoError(t, err)
	assert.Equal(t, ModeAfterHours, route.Mode)
	assert.Nil(t, id, "message-only after-hours has no profile")

	_, _, err = ForBootstrap(ctx, stubNumbers{err: domain.ErrNotFound}, stubOrgs{}, "+1", open)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, _, err = ForBootstrap(ctx, stubNumbers{}, stubOrgs{}, "+1", open)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
