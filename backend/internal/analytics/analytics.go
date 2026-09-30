// Package analytics implements the read-only reporting queries behind
// /api/analytics: overview KPIs, time series, weekday/hour heatmap, per-profile
// and per-campaign breakdowns and the calls CSV export. Everything is computed
// on demand from the calls / campaigns / campaign_targets tables.
package analytics

import (
	"context"
	"fmt"
	"io"
	"time"
	_ "time/tzdata" // deterministic zone lookups in minimal containers

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	// DefaultTimezone is used when an org has no (valid) timezone.
	DefaultTimezone = "Asia/Ulaanbaatar"
	// DefaultRange is the window used when from is omitted.
	DefaultRange = 30 * 24 * time.Hour
	// MaxRange is the widest window a query may cover.
	MaxRange = 366 * 24 * time.Hour
	// MaxExportRows caps the CSV export.
	MaxExportRows = 100_000

	BucketHour = "hour"
	BucketDay  = "day"
)

// Querier is the read port the HTTP layer depends on; *Store implements it.
type Querier interface {
	Overview(ctx context.Context, orgID uuid.UUID, from, to time.Time, tz string) (*Overview, error)
	Timeseries(ctx context.Context, orgID uuid.UUID, from, to time.Time, bucket, tz string) ([]Point, error)
	Heatmap(ctx context.Context, orgID uuid.UUID, from, to time.Time, tz string) ([]HeatmapCell, error)
	Profiles(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]ProfileStat, error)
	Campaigns(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]CampaignStat, error)
	// ExportCSV streams the calls in [from,to) as CSV and returns the row count.
	ExportCSV(ctx context.Context, orgID uuid.UUID, from, to time.Time, w io.Writer) (int, error)
}

// Overview is the KPI block of the dashboard.
type Overview struct {
	Calls          int             `json:"calls"`
	Answered       int             `json:"answered"`
	AnswerRate     float64         `json:"answerRate"` // 0..1
	AvgDurationSec float64         `json:"avgDurationSec"`
	TotalMinutes   float64         `json:"totalMinutes"`
	CostMnt        int64           `json:"costMnt"`
	CostPerCallMnt float64         `json:"costPerCallMnt"`
	Sentiment      SentimentCounts `json:"sentiment"`
	Outcomes       []OutcomeCount  `json:"outcomes"`
	ByDirection    DirectionCounts `json:"byDirection"`
}

// SentimentCounts counts calls per sentiment (unrated calls are not counted).
type SentimentCounts struct {
	Positive int `json:"positive"`
	Neutral  int `json:"neutral"`
	Negative int `json:"negative"`
}

// DirectionCounts counts calls per direction.
type DirectionCounts struct {
	Inbound  int `json:"inbound"`
	Outbound int `json:"outbound"`
}

// OutcomeCount is one campaign outcome with its label.
type OutcomeCount struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// Point is one time-series bucket. TS is the bucket start.
type Point struct {
	TS       time.Time `json:"ts"`
	Calls    int       `json:"calls"`
	Answered int       `json:"answered"`
	Minutes  float64   `json:"minutes"`
	CostMnt  int64     `json:"costMnt"`
}

// HeatmapCell is one populated weekday/hour slot (Weekday 0 = Sunday).
type HeatmapCell struct {
	Weekday    int     `json:"weekday"`
	Hour       int     `json:"hour"`
	Calls      int     `json:"calls"`
	AnswerRate float64 `json:"answerRate"`
}

// ProfileStat aggregates the calls of one agent profile.
type ProfileStat struct {
	ProfileID      uuid.UUID      `json:"profileId"`
	Name           string         `json:"name"`
	Calls          int            `json:"calls"`
	AnswerRate     float64        `json:"answerRate"`
	AvgDurationSec float64        `json:"avgDurationSec"`
	PositiveRate   float64        `json:"positiveRate"` // positive / calls with a sentiment
	CostMnt        int64          `json:"costMnt"`
	Outcomes       map[string]int `json:"outcomes"`
}

// CampaignStat aggregates one campaign. Target counts are all-time; outcomes,
// minutes and cost cover the calls inside the requested range.
type CampaignStat struct {
	CampaignID uuid.UUID      `json:"campaignId"`
	Name       string         `json:"name"`
	Total      int            `json:"total"`
	Done       int            `json:"done"`
	Failed     int            `json:"failed"`
	Skipped    int            `json:"skipped"`
	Outcomes   map[string]int `json:"outcomes"`
	Minutes    float64        `json:"minutes"`
	CostMnt    int64          `json:"costMnt"`
}

// NormalizeRange fills in defaults (to = now, from = to - 30 days) and checks
// that the window is ordered and at most MaxRange wide. The result is UTC.
func NormalizeRange(from, to, now time.Time) (time.Time, time.Time, error) {
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-DefaultRange)
	}
	if !to.After(from) {
		return from, to, fmt.Errorf("%w: from must be before to", domain.ErrInvalid)
	}
	if to.Sub(from) > MaxRange {
		return from, to, fmt.Errorf("%w: range must not exceed 366 days", domain.ErrInvalid)
	}
	return from.UTC(), to.UTC(), nil
}

// Location resolves an IANA timezone name, falling back to DefaultTimezone and
// then UTC.
func Location(tz string) *time.Location {
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	if l, err := time.LoadLocation(DefaultTimezone); err == nil {
		return l
	}
	return time.UTC
}
