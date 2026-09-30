package campaign

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	// Embed the IANA time zone database so schedule time zones (default
	// Asia/Ulaanbaatar) resolve in minimal containers without tzdata.
	_ "time/tzdata"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultTimezone is used when a campaign schedule has no Timezone.
const DefaultTimezone = "Asia/Ulaanbaatar"

// InWindow reports whether now falls inside the calling window of s.
//
//   - The schedule is evaluated in s.Timezone (DefaultTimezone when empty; an
//     unknown zone falls back to UTC).
//   - Weekdays lists the allowed days (0 = Sunday); empty = every day.
//   - StartTime / EndTime are "HH:MM"; the start is inclusive and the end
//     exclusive; an empty bound is open. EndTime < StartTime is an overnight
//     window (e.g. 22:00–06:00); its after-midnight part belongs to the day
//     the window started on, so Weekdays is checked against the previous day.
//     StartTime == EndTime is an empty window. Malformed times are ignored
//     (treated as unset); ValidateSchedule reports them.
//
// PacePerMinute does not affect the window.
func InWindow(s domain.CampaignSchedule, now time.Time) bool {
	loc, _ := loadLocation(s.Timezone)
	return inWindowIn(s, now, loc)
}

// loadLocation resolves a schedule time zone. It returns UTC and an error
// for an unknown zone.
func loadLocation(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		name = DefaultTimezone
	}
	loc, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return time.UTC, fmt.Errorf("load time zone %q: %w", name, err)
	}
	return loc, nil
}

func inWindowIn(s domain.CampaignSchedule, now time.Time, loc *time.Location) bool {
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	start, hasStart := parseClock(s.StartTime)
	end, hasEnd := parseClock(s.EndTime)

	day := local.Weekday()
	switch {
	case hasStart && hasEnd && start == end:
		return false
	case hasStart && hasEnd && end < start: // overnight
		switch {
		case minute >= start:
		case minute < end:
			day = (day + 6) % 7 // window opened the previous day
		default:
			return false
		}
	default:
		if hasStart && minute < start {
			return false
		}
		if hasEnd && minute >= end {
			return false
		}
	}
	return len(s.Weekdays) == 0 || slices.Contains(s.Weekdays, day)
}

// parseClock parses "HH:MM" (00:00–23:59, also "24:00" as end of day) into
// minutes since midnight. ok is false for an empty or malformed value.
func parseClock(v string) (int, bool) {
	v = strings.TrimSpace(v)
	h, m, found := strings.Cut(v, ":")
	if !found || len(m) != 2 || len(h) == 0 || len(h) > 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 || hh > 24 || (hh == 24 && mm != 0) {
		return 0, false
	}
	return hh*60 + mm, true
}

// ValidateSchedule checks a schedule for the HTTP layer: known time zone,
// weekdays 0–6, well-formed "HH:MM" bounds that differ, and a non-negative
// pace. Errors wrap domain.ErrInvalid.
func ValidateSchedule(s domain.CampaignSchedule) error {
	if s.Timezone != "" {
		if _, err := loadLocation(s.Timezone); err != nil {
			return fmt.Errorf("schedule timezone: %w: %w", domain.ErrInvalid, err)
		}
	}
	for _, d := range s.Weekdays {
		if d < time.Sunday || d > time.Saturday {
			return fmt.Errorf("schedule weekday %d out of range 0-6: %w", d, domain.ErrInvalid)
		}
	}
	start, hasStart := parseClock(s.StartTime)
	if s.StartTime != "" && !hasStart {
		return fmt.Errorf("schedule startTime %q is not HH:MM: %w", s.StartTime, domain.ErrInvalid)
	}
	end, hasEnd := parseClock(s.EndTime)
	if s.EndTime != "" && !hasEnd {
		return fmt.Errorf("schedule endTime %q is not HH:MM: %w", s.EndTime, domain.ErrInvalid)
	}
	if hasStart && hasEnd && start == end {
		return fmt.Errorf("schedule startTime equals endTime: %w", domain.ErrInvalid)
	}
	if s.PacePerMinute < 0 {
		return fmt.Errorf("schedule pacePerMinute must be >= 0: %w", domain.ErrInvalid)
	}
	return nil
}

// tokenBucket limits how many new dials a campaign starts per minute: it
// refills at pace/60 tokens per second up to a burst of pace tokens.
type tokenBucket struct {
	pace   int
	tokens float64
	last   time.Time
}

func newTokenBucket(pace int, now time.Time) *tokenBucket {
	return &tokenBucket{pace: pace, tokens: float64(pace), last: now}
}

// available refills the bucket up to now (adapting to a changed pace) and
// returns the whole number of tokens that may be taken.
func (b *tokenBucket) available(pace int, now time.Time) int {
	if pace != b.pace {
		b.pace = pace
		b.tokens = min(b.tokens, float64(pace))
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(float64(b.pace), b.tokens+elapsed.Seconds()*float64(b.pace)/60)
	}
	b.last = now
	return int(b.tokens)
}

// take consumes n tokens (after available).
func (b *tokenBucket) take(n int) {
	b.tokens = max(0, b.tokens-float64(n))
}
