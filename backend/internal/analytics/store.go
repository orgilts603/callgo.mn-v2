package analytics

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Store runs the analytics SQL on a pgx pool.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
	// usageCol caches that calls.usage exists (added by a later migration).
	usageCol atomic.Bool
}

var _ Querier = (*Store)(nil)

// New returns a Store on pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

const (
	answeredExpr = `(c.status IN ('completed','voicemail') OR c.answered_at IS NOT NULL)`
	rangeWhere   = `c.org_id = $1 AND c.started_at >= $2 AND c.started_at < $3`
)

// costExpr is the per-call cost in MNT. Until the migration that adds
// calls.usage has run it degrades to 0 instead of failing.
func (s *Store) costExpr(ctx context.Context) (string, error) {
	if !s.usageCol.Load() {
		var ok bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = ANY (current_schemas(false))
			  AND table_name = 'calls' AND column_name = 'usage')`).Scan(&ok)
		if err != nil {
			return "", fmt.Errorf("analytics: detect calls.usage: %w", err)
		}
		if !ok {
			return "0::bigint", nil
		}
		s.usageCol.Store(true)
	}
	return `COALESCE(round((c.usage->>'costMnt')::numeric)::bigint, 0)`, nil
}

// HasUsageColumn reports whether cost data is available (calls.usage exists).
func (s *Store) HasUsageColumn(ctx context.Context) (bool, error) {
	e, err := s.costExpr(ctx)
	return e != "0::bigint", err
}

// labelExpr resolves an outcome code to the label configured on campaign cp.
func labelExpr(code, campaign string) string {
	return fmt.Sprintf(`COALESCE((SELECT o->>'label'
		FROM jsonb_array_elements(CASE WHEN jsonb_typeof(%[2]s.outcomes) = 'array' THEN %[2]s.outcomes ELSE '[]'::jsonb END) o
		WHERE o->>'code' = %[1]s AND COALESCE(o->>'label','') <> '' LIMIT 1), %[1]s)`, code, campaign)
}

func (s *Store) window(from, to time.Time, tz string) (time.Time, time.Time, *time.Location, error) {
	f, t, err := NormalizeRange(from, to, s.now())
	if err != nil {
		return f, t, nil, err
	}
	return f, t, Location(tz), nil
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

func rate(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return round(float64(n)/float64(d), 4)
}

// Overview returns the KPI block for [from,to).
func (s *Store) Overview(ctx context.Context, orgID uuid.UUID, from, to time.Time, tz string) (*Overview, error) {
	from, to, _, err := s.window(from, to, tz)
	if err != nil {
		return nil, err
	}
	cost, err := s.costExpr(ctx)
	if err != nil {
		return nil, err
	}
	var (
		o        Overview
		durSum   int64
		ansDur   float64
		totalDur int64
	)
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE `+answeredExpr+`),
		       COALESCE(avg(c.duration_sec) FILTER (WHERE `+answeredExpr+`), 0)::float8,
		       COALESCE(sum(c.duration_sec), 0)::bigint,
		       COALESCE(sum(`+cost+`), 0)::bigint,
		       count(*) FILTER (WHERE c.sentiment = 'positive'),
		       count(*) FILTER (WHERE c.sentiment = 'neutral'),
		       count(*) FILTER (WHERE c.sentiment = 'negative'),
		       count(*) FILTER (WHERE c.direction = 'inbound'),
		       count(*) FILTER (WHERE c.direction = 'outbound')
		FROM calls c WHERE `+rangeWhere, orgID, from, to).Scan(
		&o.Calls, &o.Answered, &ansDur, &durSum, &o.CostMnt,
		&o.Sentiment.Positive, &o.Sentiment.Neutral, &o.Sentiment.Negative,
		&o.ByDirection.Inbound, &o.ByDirection.Outbound)
	if err != nil {
		return nil, fmt.Errorf("analytics: overview: %w", err)
	}
	totalDur = durSum
	o.AnswerRate = rate(o.Answered, o.Calls)
	o.AvgDurationSec = round(ansDur, 1)
	o.TotalMinutes = round(float64(totalDur)/60, 2)
	if o.Calls > 0 {
		o.CostPerCallMnt = round(float64(o.CostMnt)/float64(o.Calls), 2)
	}

	// Outcome labels come from the org's campaign definitions (newest wins).
	rows, err := s.pool.Query(ctx, `
		WITH labels AS (
			SELECT DISTINCT ON (o->>'code') o->>'code' AS code, o->>'label' AS label
			FROM campaigns cp,
			     jsonb_array_elements(CASE WHEN jsonb_typeof(cp.outcomes) = 'array' THEN cp.outcomes ELSE '[]'::jsonb END) o
			WHERE cp.org_id = $1 AND COALESCE(o->>'label','') <> ''
			ORDER BY o->>'code', cp.created_at DESC
		)
		SELECT c.outcome, COALESCE(l.label, c.outcome), count(*)::int
		FROM calls c LEFT JOIN labels l ON l.code = c.outcome
		WHERE `+rangeWhere+` AND c.outcome <> ''
		GROUP BY c.outcome, l.label
		ORDER BY count(*) DESC, c.outcome`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: overview outcomes: %w", err)
	}
	defer rows.Close()
	o.Outcomes = []OutcomeCount{}
	for rows.Next() {
		var oc OutcomeCount
		if err := rows.Scan(&oc.Code, &oc.Label, &oc.Count); err != nil {
			return nil, fmt.Errorf("analytics: scan outcome: %w", err)
		}
		o.Outcomes = append(o.Outcomes, oc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: overview outcomes: %w", err)
	}
	return &o, nil
}

// Timeseries returns one Point per bucket in [from,to) in the org timezone.
// Empty buckets are included (zero counts) so charts have a continuous axis.
func (s *Store) Timeseries(ctx context.Context, orgID uuid.UUID, from, to time.Time, bucket, tz string) ([]Point, error) {
	if bucket == "" {
		bucket = BucketDay
	}
	if bucket != BucketHour && bucket != BucketDay {
		return nil, fmt.Errorf("%w: bucket must be hour or day", domain.ErrInvalid)
	}
	from, to, loc, err := s.window(from, to, tz)
	if err != nil {
		return nil, err
	}
	cost, err := s.costExpr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT date_trunc($4, c.started_at AT TIME ZONE $5) AT TIME ZONE $5 AS ts,
		       count(*)::int,
		       (count(*) FILTER (WHERE `+answeredExpr+`))::int,
		       COALESCE(sum(c.duration_sec), 0)::float8 / 60.0,
		       COALESCE(sum(`+cost+`), 0)::bigint
		FROM calls c WHERE `+rangeWhere+`
		GROUP BY 1 ORDER BY 1`, orgID, from, to, bucket, loc.String())
	if err != nil {
		return nil, fmt.Errorf("analytics: timeseries: %w", err)
	}
	defer rows.Close()
	got := map[int64]Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.TS, &p.Calls, &p.Answered, &p.Minutes, &p.CostMnt); err != nil {
			return nil, fmt.Errorf("analytics: scan point: %w", err)
		}
		p.Minutes = round(p.Minutes, 2)
		got[p.TS.Unix()] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: timeseries: %w", err)
	}

	out := []Point{}
	for t := truncate(from.In(loc), bucket); t.Before(to); t = next(t, bucket) {
		p, ok := got[t.Unix()]
		if !ok {
			p = Point{}
		}
		p.TS = t.UTC()
		out = append(out, p)
	}
	return out, nil
}

func truncate(t time.Time, bucket string) time.Time {
	if bucket == BucketHour {
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func next(t time.Time, bucket string) time.Time {
	if bucket == BucketHour {
		return t.Add(time.Hour)
	}
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
}

// Heatmap returns the populated weekday (0 = Sunday) / hour cells in the org
// timezone.
func (s *Store) Heatmap(ctx context.Context, orgID uuid.UUID, from, to time.Time, tz string) ([]HeatmapCell, error) {
	from, to, loc, err := s.window(from, to, tz)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT extract(dow  FROM c.started_at AT TIME ZONE $4)::int,
		       extract(hour FROM c.started_at AT TIME ZONE $4)::int,
		       count(*)::int,
		       (count(*) FILTER (WHERE `+answeredExpr+`))::int
		FROM calls c WHERE `+rangeWhere+`
		GROUP BY 1, 2 ORDER BY 1, 2`, orgID, from, to, loc.String())
	if err != nil {
		return nil, fmt.Errorf("analytics: heatmap: %w", err)
	}
	defer rows.Close()
	out := []HeatmapCell{}
	for rows.Next() {
		var c HeatmapCell
		var answered int
		if err := rows.Scan(&c.Weekday, &c.Hour, &c.Calls, &answered); err != nil {
			return nil, fmt.Errorf("analytics: scan cell: %w", err)
		}
		c.AnswerRate = rate(answered, c.Calls)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: heatmap: %w", err)
	}
	return out, nil
}

// Profiles aggregates calls per agent profile (profiles without calls in the
// range are omitted).
func (s *Store) Profiles(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]ProfileStat, error) {
	from, to, _, err := s.window(from, to, "")
	if err != nil {
		return nil, err
	}
	cost, err := s.costExpr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, count(*)::int,
		       (count(*) FILTER (WHERE `+answeredExpr+`))::int,
		       COALESCE(avg(c.duration_sec) FILTER (WHERE `+answeredExpr+`), 0)::float8,
		       (count(*) FILTER (WHERE c.sentiment = 'positive'))::int,
		       (count(*) FILTER (WHERE c.sentiment <> ''))::int,
		       COALESCE(sum(`+cost+`), 0)::bigint
		FROM calls c JOIN agent_profiles p ON p.id = c.agent_profile_id AND p.org_id = c.org_id
		WHERE `+rangeWhere+`
		GROUP BY p.id, p.name
		ORDER BY count(*) DESC, p.name, p.id`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: profiles: %w", err)
	}
	defer rows.Close()
	out := []ProfileStat{}
	idx := map[uuid.UUID]int{}
	for rows.Next() {
		var p ProfileStat
		var answered, positive, rated int
		var avg float64
		if err := rows.Scan(&p.ProfileID, &p.Name, &p.Calls, &answered, &avg, &positive, &rated, &p.CostMnt); err != nil {
			return nil, fmt.Errorf("analytics: scan profile: %w", err)
		}
		p.AnswerRate = rate(answered, p.Calls)
		p.AvgDurationSec = round(avg, 1)
		p.PositiveRate = rate(positive, rated)
		p.Outcomes = map[string]int{}
		idx[p.ProfileID] = len(out)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: profiles: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}

	orows, err := s.pool.Query(ctx, `
		SELECT c.agent_profile_id, c.outcome, count(*)::int
		FROM calls c
		WHERE `+rangeWhere+` AND c.agent_profile_id IS NOT NULL AND c.outcome <> ''
		GROUP BY 1, 2`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: profile outcomes: %w", err)
	}
	defer orows.Close()
	for orows.Next() {
		var id uuid.UUID
		var code string
		var n int
		if err := orows.Scan(&id, &code, &n); err != nil {
			return nil, fmt.Errorf("analytics: scan profile outcome: %w", err)
		}
		if i, ok := idx[id]; ok {
			out[i].Outcomes[code] = n
		}
	}
	if err := orows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: profile outcomes: %w", err)
	}
	return out, nil
}

// Campaigns aggregates campaigns that have calls in the range or were created
// in it, newest first.
func (s *Store) Campaigns(ctx context.Context, orgID uuid.UUID, from, to time.Time) ([]CampaignStat, error) {
	from, to, _, err := s.window(from, to, "")
	if err != nil {
		return nil, err
	}
	cost, err := s.costExpr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT cp.id, cp.name, t.total, t.done, t.failed, t.skipped, k.minutes, k.cost
		FROM campaigns cp
		CROSS JOIN LATERAL (
			SELECT count(*)::int AS total,
			       (count(*) FILTER (WHERE status = 'done'))::int    AS done,
			       (count(*) FILTER (WHERE status = 'failed'))::int  AS failed,
			       (count(*) FILTER (WHERE status = 'skipped'))::int AS skipped
			FROM campaign_targets WHERE campaign_id = cp.id) t
		CROSS JOIN LATERAL (
			SELECT count(*) AS n,
			       COALESCE(sum(c.duration_sec), 0)::float8 / 60.0 AS minutes,
			       COALESCE(sum(`+cost+`), 0)::bigint AS cost
			FROM calls c WHERE c.campaign_id = cp.id AND `+rangeWhere+`) k
		WHERE cp.org_id = $1 AND (k.n > 0 OR (cp.created_at >= $2 AND cp.created_at < $3))
		ORDER BY cp.created_at DESC, cp.id`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: campaigns: %w", err)
	}
	defer rows.Close()
	out := []CampaignStat{}
	idx := map[uuid.UUID]int{}
	for rows.Next() {
		var c CampaignStat
		if err := rows.Scan(&c.CampaignID, &c.Name, &c.Total, &c.Done, &c.Failed, &c.Skipped, &c.Minutes, &c.CostMnt); err != nil {
			return nil, fmt.Errorf("analytics: scan campaign: %w", err)
		}
		c.Minutes = round(c.Minutes, 2)
		c.Outcomes = map[string]int{}
		idx[c.CampaignID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: campaigns: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}
	orows, err := s.pool.Query(ctx, `
		SELECT c.campaign_id, c.outcome, count(*)::int
		FROM calls c
		WHERE `+rangeWhere+` AND c.campaign_id IS NOT NULL AND c.outcome <> ''
		GROUP BY 1, 2`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("analytics: campaign outcomes: %w", err)
	}
	defer orows.Close()
	for orows.Next() {
		var id uuid.UUID
		var code string
		var n int
		if err := orows.Scan(&id, &code, &n); err != nil {
			return nil, fmt.Errorf("analytics: scan campaign outcome: %w", err)
		}
		if i, ok := idx[id]; ok {
			out[i].Outcomes[code] = n
		}
	}
	if err := orows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: campaign outcomes: %w", err)
	}
	return out, nil
}

// CSVHeader is the column order of ExportCSV.
var CSVHeader = []string{
	"startedAt", "direction", "from", "to", "contact", "status", "durationSec",
	"outcome", "outcomeNote", "sentiment", "intent", "summary", "campaign",
	"profile", "llmModel", "costMnt", "recordingUrl",
}

// ExportCSV streams the calls started in [from,to) (newest first, at most
// MaxExportRows) as UTF-8 CSV with a BOM so Excel detects the encoding. It
// returns the number of data rows written. Nothing is written when the query
// fails up front.
func (s *Store) ExportCSV(ctx context.Context, orgID uuid.UUID, from, to time.Time, w io.Writer) (int, error) {
	from, to, _, err := s.window(from, to, "")
	if err != nil {
		return 0, err
	}
	cost, err := s.costExpr(ctx)
	if err != nil {
		return 0, err
	}
	model := `c.llm_model_used`
	if cost != "0::bigint" {
		model = `COALESCE(NULLIF(c.llm_model_used, ''), c.usage->>'llmModel', '')`
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.started_at, c.direction, c.from_number, c.to_number,
		       COALESCE(NULLIF(ct.name, ''), ct.phone, ''),
		       c.status, c.duration_sec,
		       `+labelExpr("c.outcome", "cp")+`,
		       c.outcome_note, c.sentiment, c.intent, c.summary,
		       COALESCE(cp.name, ''), COALESCE(ap.name, ''),
		       `+model+`, `+cost+`, c.recording_url
		FROM calls c
		LEFT JOIN contacts ct       ON ct.id = c.contact_id
		LEFT JOIN campaigns cp      ON cp.id = c.campaign_id
		LEFT JOIN agent_profiles ap ON ap.id = c.agent_profile_id
		WHERE `+rangeWhere+`
		ORDER BY c.started_at DESC, c.id
		LIMIT $4`, orgID, from, to, MaxExportRows)
	if err != nil {
		return 0, fmt.Errorf("analytics: export: %w", err)
	}
	defer rows.Close()

	if _, err := io.WriteString(w, utf8BOM); err != nil {
		return 0, fmt.Errorf("analytics: export write: %w", err)
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return 0, fmt.Errorf("analytics: export write: %w", err)
	}
	n := 0
	for rows.Next() {
		var (
			started                                                    time.Time
			direction, fromN, toN, contact, status                     string
			dur                                                        int
			outcome, note, sentiment, intent, summary, camp, prof, mdl string
			costMnt                                                    int64
			rec                                                        string
		)
		if err := rows.Scan(&started, &direction, &fromN, &toN, &contact, &status, &dur,
			&outcome, &note, &sentiment, &intent, &summary, &camp, &prof, &mdl, &costMnt, &rec); err != nil {
			return n, fmt.Errorf("analytics: export scan: %w", err)
		}
		rec2 := []string{
			started.UTC().Format(time.RFC3339), direction, fromN, toN, safeCell(contact), status,
			strconv.Itoa(dur), safeCell(outcome), safeCell(note), sentiment, safeCell(intent),
			safeCell(summary), safeCell(camp), safeCell(prof), mdl, strconv.FormatInt(costMnt, 10), rec,
		}
		if err := cw.Write(rec2); err != nil {
			return n, fmt.Errorf("analytics: export write: %w", err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("analytics: export: %w", err)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return n, fmt.Errorf("analytics: export flush: %w", err)
	}
	return n, nil
}

// safeCell neutralises spreadsheet formula injection in free-text cells.
func safeCell(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

const utf8BOM = "\xef\xbb\xbf"
