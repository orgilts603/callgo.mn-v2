package campaign

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestInWindow(t *testing.T) {
	ub, err := time.LoadLocation("Asia/Ulaanbaatar")
	require.NoError(t, err)
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	// 2026-09-30 is a Wednesday.
	at := func(loc *time.Location, day, hour, minute int) time.Time {
		return time.Date(2026, 9, day, hour, minute, 0, 0, loc)
	}
	weekdays := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	office := domain.CampaignSchedule{Weekdays: weekdays, StartTime: "09:00", EndTime: "18:00"}

	cases := []struct {
		name string
		s    domain.CampaignSchedule
		now  time.Time
		want bool
	}{
		{"zero schedule is always open", domain.CampaignSchedule{}, at(ub, 27, 3, 0), true},
		{"pace only is always open", domain.CampaignSchedule{PacePerMinute: 5}, at(ub, 27, 3, 0), true},
		{"default tz UB: start inclusive", office, at(ub, 30, 9, 0), true},
		{"default tz UB: inside", office, at(ub, 30, 12, 30), true},
		{"default tz UB: before start", office, at(ub, 30, 8, 59), false},
		{"default tz UB: end exclusive", office, at(ub, 30, 18, 0), false},
		{"default tz UB: last minute", office, at(ub, 30, 17, 59), true},
		// 02:00 UTC = 10:00 in Ulaanbaatar (UTC+8).
		{"UTC instant converted to UB", office, time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC), true},
		// 10:00 UTC = 18:00 in Ulaanbaatar.
		{"UTC instant after UB close", office, time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), false},
		{"saturday closed", office, at(ub, 26, 12, 0), false},
		{"sunday closed", office, at(ub, 27, 12, 0), false},
		{"weekend-only schedule on sunday", domain.CampaignSchedule{Weekdays: []time.Weekday{time.Saturday, time.Sunday}}, at(ub, 27, 23, 0), true},
		{"weekend-only schedule on monday", domain.CampaignSchedule{Weekdays: []time.Weekday{time.Saturday, time.Sunday}}, at(ub, 28, 0, 30), false},
		// Friday 23:00 UTC is Saturday 07:00 in Ulaanbaatar.
		{"weekday evaluated in the schedule tz", domain.CampaignSchedule{Weekdays: []time.Weekday{time.Saturday}}, time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC), true},
		{"explicit tz New York", domain.CampaignSchedule{Timezone: "America/New_York", StartTime: "09:00", EndTime: "17:00"}, at(ny, 30, 9, 30), true},
		{"explicit tz New York closed", domain.CampaignSchedule{Timezone: "America/New_York", StartTime: "09:00", EndTime: "17:00"}, at(ub, 30, 12, 0), false},
		{"invalid tz treated as UTC: open", domain.CampaignSchedule{Timezone: "Mars/Olympus", StartTime: "09:00", EndTime: "17:00"}, time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), true},
		{"invalid tz treated as UTC: closed", domain.CampaignSchedule{Timezone: "Mars/Olympus", StartTime: "09:00", EndTime: "17:00"}, time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC), false},
		{"start only", domain.CampaignSchedule{StartTime: "10:00"}, at(ub, 30, 23, 59), true},
		{"start only before", domain.CampaignSchedule{StartTime: "10:00"}, at(ub, 30, 9, 59), false},
		{"end only", domain.CampaignSchedule{EndTime: "10:00"}, at(ub, 30, 0, 0), true},
		{"end only after", domain.CampaignSchedule{EndTime: "10:00"}, at(ub, 30, 10, 0), false},
		{"overnight evening", domain.CampaignSchedule{StartTime: "22:00", EndTime: "06:00"}, at(ub, 30, 23, 0), true},
		{"overnight early morning", domain.CampaignSchedule{StartTime: "22:00", EndTime: "06:00"}, at(ub, 30, 5, 59), true},
		{"overnight end exclusive", domain.CampaignSchedule{StartTime: "22:00", EndTime: "06:00"}, at(ub, 30, 6, 0), false},
		{"overnight midday closed", domain.CampaignSchedule{StartTime: "22:00", EndTime: "06:00"}, at(ub, 30, 12, 0), false},
		// Overnight windows belong to the day they start on: Friday 22:00 →
		// Saturday 06:00 is open on a weekday-only schedule, Sunday night →
		// Monday morning is not.
		{"overnight friday night into saturday", domain.CampaignSchedule{Weekdays: weekdays, StartTime: "22:00", EndTime: "06:00"}, at(ub, 26, 2, 0), true},
		{"overnight sunday night into monday", domain.CampaignSchedule{Weekdays: weekdays, StartTime: "22:00", EndTime: "06:00"}, at(ub, 28, 2, 0), false},
		{"overnight saturday evening", domain.CampaignSchedule{Weekdays: weekdays, StartTime: "22:00", EndTime: "06:00"}, at(ub, 26, 22, 30), false},
		{"equal bounds is empty", domain.CampaignSchedule{StartTime: "09:00", EndTime: "09:00"}, at(ub, 30, 9, 0), false},
		{"malformed bound ignored", domain.CampaignSchedule{StartTime: "9am", EndTime: "18:00"}, at(ub, 30, 3, 0), true},
		{"24:00 end", domain.CampaignSchedule{StartTime: "20:00", EndTime: "24:00"}, at(ub, 30, 23, 59), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, InWindow(tc.s, tc.now))
		})
	}
}

func TestValidateSchedule(t *testing.T) {
	valid := []domain.CampaignSchedule{
		{},
		{Timezone: "Asia/Ulaanbaatar", Weekdays: []time.Weekday{0, 6}, StartTime: "09:00", EndTime: "18:00", PacePerMinute: 10},
		{StartTime: "22:00", EndTime: "06:00"},
		{StartTime: "9:30"},
		{EndTime: "24:00"},
	}
	for _, s := range valid {
		assert.NoError(t, ValidateSchedule(s), "%+v", s)
	}
	invalid := []domain.CampaignSchedule{
		{Timezone: "Nowhere/Land"},
		{Weekdays: []time.Weekday{7}},
		{Weekdays: []time.Weekday{-1}},
		{StartTime: "9"},
		{StartTime: "25:00"},
		{EndTime: "12:60"},
		{EndTime: "24:30"},
		{StartTime: "10:00", EndTime: "10:00"},
		{PacePerMinute: -1},
	}
	for _, s := range invalid {
		assert.ErrorIs(t, ValidateSchedule(s), domain.ErrInvalid, "%+v", s)
	}
}

func TestTokenBucket(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	b := newTokenBucket(6, t0)
	assert.Equal(t, 6, b.available(6, t0), "starts with a full burst")
	b.take(6)
	assert.Equal(t, 0, b.available(6, t0))
	assert.Equal(t, 0, b.available(6, t0.Add(9*time.Second)))
	assert.Equal(t, 1, b.available(6, t0.Add(10*time.Second)), "6/min = one token per 10s")
	assert.Equal(t, 6, b.available(6, t0.Add(time.Hour)), "capped at the burst")
	b.take(2)
	assert.Equal(t, 2, b.available(2, t0.Add(time.Hour)), "pace lowered: burst shrinks")
	b.take(10)
	assert.Equal(t, 0, b.available(2, t0.Add(time.Hour)))
}
