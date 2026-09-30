package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestAddMonth(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 10, 30, 0, 0, time.UTC) }
	assert.Equal(t, d(2026, 10, 1), addMonth(d(2026, 9, 1)))
	assert.Equal(t, d(2026, 2, 28), addMonth(d(2026, 1, 31)))
	assert.Equal(t, d(2028, 2, 29), addMonth(d(2028, 1, 30)))
	assert.Equal(t, d(2027, 1, 15), addMonth(d(2026, 12, 15)))
	assert.Equal(t, d(2026, 4, 30), addMonth(d(2026, 3, 31)))
}

func TestBillableMinutes(t *testing.T) {
	now := time.Now()
	ans := now.Add(-90 * time.Second)
	cases := []struct {
		name string
		call domain.Call
		want int
	}{
		{"unanswered", domain.Call{}, 0},
		{"answered zero seconds", domain.Call{AnsweredAt: &now}, 1},
		{"exactly 60s", domain.Call{AnsweredAt: &now, DurationSec: 60}, 1},
		{"61s", domain.Call{AnsweredAt: &now, DurationSec: 61}, 2},
		{"duration without answeredAt", domain.Call{DurationSec: 125}, 3},
		{"derived from timestamps", domain.Call{AnsweredAt: &ans, EndedAt: &now}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, BillableMinutes(&c.call)) })
	}
}
