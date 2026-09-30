package analytics

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/crm"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const defaultTestDSN = "postgres://callgo:callgo@localhost:5432/callgo_test?sslmode=disable"

var (
	testPool *pgxpool.Pool
	testDSN  string
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	dsn := os.Getenv("CALLGO_TEST_DATABASE_URL")
	explicit := dsn != ""
	if !explicit {
		dsn = defaultTestDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := crm.Open(ctx, dsn)
	if err != nil {
		if explicit {
			fmt.Fprintf(os.Stderr, "analytics tests: cannot connect: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "analytics tests: no database, integration tests skipped: %v\n", err)
		return m.Run()
	}
	defer pool.Close()
	// Idempotent; other packages' tests may reset the schema concurrently, so
	// tests only ever touch rows of their own organisation.
	if err := crm.Migrate(ctx, dsn); err != nil {
		fmt.Fprintf(os.Stderr, "analytics tests: migrate: %v\n", err)
		return 1
	}
	testPool = pool
	testDSN = dsn
	return m.Run()
}

type fixture struct {
	ctx      context.Context
	store    *Store
	org      uuid.UUID
	p1, p2   uuid.UUID
	c1, c2   uuid.UUID
	from, to time.Time
	hasCost  bool
}

// seed creates an isolated org with a fixed data set and removes it on cleanup.
// Other packages' tests share the database and may truncate every table (or
// reset the schema), so the whole data set is inserted in one transaction and
// retried when that happens.
func seed(t *testing.T) *fixture {
	t.Helper()
	if testPool == nil {
		t.Skip("no database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		f := &fixture{ctx: ctx, store: New(testPool), org: uuid.New(), p1: uuid.New(), p2: uuid.New(), c1: uuid.New(), c2: uuid.New()}
		f.from = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		f.to = time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
		if lastErr = seedOnce(ctx, f); lastErr == nil {
			t.Cleanup(func() {
				c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _ = testPool.Exec(c, `DELETE FROM organizations WHERE id = $1 OR slug = $2`, f.org, "an-other-"+f.org.String())
			})
			return f
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
		_ = crm.Migrate(ctx, testDSN)
	}
	require.NoError(t, lastErr)
	return nil
}

func seedOnce(ctx context.Context, f *fixture) error {
	has, err := f.store.HasUsageColumn(ctx)
	if err != nil {
		return err
	}
	f.hasCost = has
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var execErr error
	exec := func(sql string, args ...any) {
		if execErr != nil {
			return
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			execErr = err
		}
	}

	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, 'An', $2)`, f.org, "an-"+f.org.String())
	exec(`INSERT INTO agent_profiles (id, org_id, name) VALUES ($1,$2,'Sales'), ($3,$2,'Support')`, f.p1, f.org, f.p2)
	exec(`INSERT INTO campaigns (id, org_id, name, outcomes) VALUES ($1,$2,'Spring', $3::jsonb), ($4,$2,'Empty', '[]')`,
		f.c1, f.org, `[{"code":"agreed","label":"Зөвшөөрсөн","terminal":true}]`, f.c2)
	exec(`INSERT INTO campaign_targets (campaign_id, pos, phone, status) VALUES
		($1,0,'+97611','done'), ($1,1,'+97622','failed'), ($1,2,'+97633','skipped'), ($1,3,'+97644','pending')`, f.c1)
	contact := uuid.New()
	exec(`INSERT INTO contacts (id, org_id, phone, name) VALUES ($1,$2,'+97699','Bat')`, contact, f.org)

	costs := map[uuid.UUID]int{}
	ins := func(dir, status string, started time.Time, dur int, answered bool, sentiment, outcome string, camp, prof *uuid.UUID, summary string, contactID *uuid.UUID, cost int) {
		id := uuid.New()
		var ans *time.Time
		if answered {
			a := started.Add(time.Second)
			ans = &a
		}
		exec(`INSERT INTO calls (id, org_id, contact_id, campaign_id, agent_profile_id, direction, status, from_number, to_number,
			started_at, answered_at, duration_sec, sentiment, outcome, summary, recording_url, llm_model_used)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'+97670','+97688',$8,$9,$10,$11,$12,$13,'/api/calls/x/recording','gpt-x')`,
			id, f.org, contactID, camp, prof, dir, status, started, ans, dur, sentiment, outcome, summary)
		if cost > 0 {
			costs[id] = cost
		}
	}
	// Asia/Ulaanbaatar is UTC+8: 02:00Z = 10:00 Monday 2026-03-02.
	c1, p1, p2 := f.c1, f.p1, f.p2
	ins("inbound", "completed", time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC), 120, true, "positive", "", nil, &p1, "=cmd()", &contact, 100)
	ins("outbound", "completed", time.Date(2026, 3, 2, 2, 30, 0, 0, time.UTC), 60, true, "negative", "agreed", &c1, &p1, "ok", nil, 50)
	ins("outbound", "no_answer", time.Date(2026, 3, 2, 17, 0, 0, 0, time.UTC), 0, false, "", "", &c1, &p1, "", nil, 0) // Tue 01:00 local
	ins("outbound", "voicemail", time.Date(2026, 3, 3, 3, 0, 0, 0, time.UTC), 30, false, "", "mystery", &c1, nil, "", nil, 20)
	ins("outbound", "failed", time.Date(2026, 3, 3, 3, 30, 0, 0, time.UTC), 90, true, "neutral", "", nil, &p2, "", nil, 30)
	// Outside the range and another org's rows must never be counted.
	ins("inbound", "completed", time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC), 500, true, "positive", "", nil, &p1, "", nil, 999)
	other := uuid.New()
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1,'Other',$2)`, other, "an-other-"+f.org.String())
	exec(`INSERT INTO calls (org_id, direction, status, started_at, duration_sec) VALUES ($1,'inbound','completed',$2,999)`,
		other, time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC))
	if has {
		for id, cost := range costs {
			exec(`UPDATE calls SET usage = jsonb_build_object('costMnt', $2::int, 'llmModel', 'from-usage') WHERE id = $1`, id, cost)
		}
	}
	if execErr != nil {
		return execErr
	}
	return tx.Commit(ctx)
}

func TestOverview(t *testing.T) {
	f := seed(t)
	o, err := f.store.Overview(f.ctx, f.org, f.from, f.to, "Asia/Ulaanbaatar")
	require.NoError(t, err)
	require.Equal(t, 5, o.Calls)
	require.Equal(t, 4, o.Answered)
	require.InDelta(t, 0.8, o.AnswerRate, 1e-9)
	require.InDelta(t, 75.0, o.AvgDurationSec, 1e-9)
	require.InDelta(t, 5.0, o.TotalMinutes, 1e-9)
	require.Equal(t, SentimentCounts{Positive: 1, Neutral: 1, Negative: 1}, o.Sentiment)
	require.Equal(t, DirectionCounts{Inbound: 1, Outbound: 4}, o.ByDirection)
	require.Equal(t, []OutcomeCount{{"agreed", "Зөвшөөрсөн", 1}, {"mystery", "mystery", 1}}, o.Outcomes)
	if f.hasCost {
		require.Equal(t, int64(200), o.CostMnt)
		require.InDelta(t, 40.0, o.CostPerCallMnt, 1e-9)
	} else {
		require.Zero(t, o.CostMnt)
	}
}

func TestOverviewEmptyAndDefaults(t *testing.T) {
	f := seed(t)
	o, err := f.store.Overview(f.ctx, uuid.New(), time.Time{}, time.Time{}, "")
	require.NoError(t, err)
	require.Zero(t, o.Calls)
	require.Zero(t, o.AnswerRate)
	require.NotNil(t, o.Outcomes)
	require.Empty(t, o.Outcomes)
}

func TestRangeValidation(t *testing.T) {
	f := seed(t)
	_, err := f.store.Overview(f.ctx, f.org, f.to, f.from, "")
	require.True(t, errors.Is(err, domain.ErrInvalid))
	_, err = f.store.Heatmap(f.ctx, f.org, f.from, f.from.Add(367*24*time.Hour), "")
	require.True(t, errors.Is(err, domain.ErrInvalid))
	_, err = f.store.Timeseries(f.ctx, f.org, f.from, f.to, "week", "")
	require.True(t, errors.Is(err, domain.ErrInvalid))
	_, err = f.store.Campaigns(f.ctx, f.org, f.from, f.from.Add(366*24*time.Hour))
	require.NoError(t, err)
}

func TestTimeseriesDay(t *testing.T) {
	f := seed(t)
	pts, err := f.store.Timeseries(f.ctx, f.org, f.from, f.to, "day", "Asia/Ulaanbaatar")
	require.NoError(t, err)
	loc := Location("Asia/Ulaanbaatar")
	byDay := map[string]Point{}
	for _, p := range pts {
		byDay[p.TS.In(loc).Format("2006-01-02")] = p
		require.Equal(t, 0, p.TS.In(loc).Hour(), "bucket starts at local midnight")
	}
	require.Len(t, pts, 8) // Mar 1..8 local, empty days included
	require.Equal(t, 2, byDay["2026-03-02"].Calls)
	require.Equal(t, 2, byDay["2026-03-02"].Answered)
	require.InDelta(t, 3.0, byDay["2026-03-02"].Minutes, 1e-9)
	// 17:00Z on Mar 2 is Mar 3 01:00 local; 03:00Z/03:30Z on Mar 3 are 11:00/11:30 local.
	require.Equal(t, 3, byDay["2026-03-03"].Calls)
	require.Zero(t, byDay["2026-03-04"].Calls)
}

func TestTimeseriesHour(t *testing.T) {
	f := seed(t)
	pts, err := f.store.Timeseries(f.ctx, f.org, f.from, f.from.Add(3*24*time.Hour), "hour", "UTC")
	require.NoError(t, err)
	require.Len(t, pts, 72)
	total := 0
	for _, p := range pts {
		total += p.Calls
		if p.TS.Equal(time.Date(2026, 3, 2, 2, 0, 0, 0, time.UTC)) {
			require.Equal(t, 2, p.Calls)
			require.Equal(t, 2, p.Answered)
		}
	}
	require.Equal(t, 5, total)
}

func TestHeatmap(t *testing.T) {
	f := seed(t)
	cells, err := f.store.Heatmap(f.ctx, f.org, f.from, f.to, "Asia/Ulaanbaatar")
	require.NoError(t, err)
	got := map[[2]int]HeatmapCell{}
	for _, c := range cells {
		got[[2]int{c.Weekday, c.Hour}] = c
	}
	// Monday 10:00 local: two answered calls.
	require.Equal(t, HeatmapCell{Weekday: 1, Hour: 10, Calls: 2, AnswerRate: 1}, got[[2]int{1, 10}])
	// Tuesday 01:00 local: the unanswered call.
	require.Equal(t, HeatmapCell{Weekday: 2, Hour: 1, Calls: 1, AnswerRate: 0}, got[[2]int{2, 1}])
	// Tuesday 11:00 local: voicemail (answered) + failed with answered_at.
	require.Equal(t, HeatmapCell{Weekday: 2, Hour: 11, Calls: 2, AnswerRate: 1}, got[[2]int{2, 11}])
	require.Len(t, cells, 3)

	utc, err := f.store.Heatmap(f.ctx, f.org, f.from, f.to, "UTC")
	require.NoError(t, err)
	found := false
	for _, c := range utc {
		if c.Weekday == 1 && c.Hour == 2 {
			found = true
		}
	}
	require.True(t, found, "same calls land in different cells under UTC")
}

func TestProfiles(t *testing.T) {
	f := seed(t)
	ps, err := f.store.Profiles(f.ctx, f.org, f.from, f.to)
	require.NoError(t, err)
	require.Len(t, ps, 2)
	sales, support := ps[0], ps[1]
	require.Equal(t, f.p1, sales.ProfileID)
	require.Equal(t, "Sales", sales.Name)
	require.Equal(t, 3, sales.Calls)
	require.InDelta(t, 2.0/3, sales.AnswerRate, 1e-3)
	require.InDelta(t, 90.0, sales.AvgDurationSec, 1e-9)
	require.InDelta(t, 0.5, sales.PositiveRate, 1e-9) // 1 positive of 2 rated
	require.Equal(t, map[string]int{"agreed": 1}, sales.Outcomes)
	require.Equal(t, "Support", support.Name)
	require.Equal(t, 1, support.Calls)
	require.Equal(t, 0.0, support.PositiveRate)
	require.NotNil(t, support.Outcomes)
	require.Empty(t, support.Outcomes)
	if f.hasCost {
		require.Equal(t, int64(150), sales.CostMnt)
		require.Equal(t, int64(30), support.CostMnt)
	}
}

func TestCampaigns(t *testing.T) {
	f := seed(t)
	cs, err := f.store.Campaigns(f.ctx, f.org, f.from, f.to)
	require.NoError(t, err)
	// "Empty" was created now, outside the March window, and has no calls.
	require.Len(t, cs, 1)
	c := cs[0]
	require.Equal(t, f.c1, c.CampaignID)
	require.Equal(t, "Spring", c.Name)
	require.Equal(t, 4, c.Total)
	require.Equal(t, 1, c.Done)
	require.Equal(t, 1, c.Failed)
	require.Equal(t, 1, c.Skipped)
	require.Equal(t, map[string]int{"agreed": 1, "mystery": 1}, c.Outcomes)
	require.InDelta(t, 1.5, c.Minutes, 1e-9)
	if f.hasCost {
		require.Equal(t, int64(70), c.CostMnt)
	}

	// A window that contains the campaign creation time lists both.
	now := time.Now()
	cs, err = f.store.Campaigns(f.ctx, f.org, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, cs, 2)
}

func TestExportCSV(t *testing.T) {
	f := seed(t)
	var buf bytes.Buffer
	n, err := f.store.ExportCSV(f.ctx, f.org, f.from, f.to, &buf)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	raw := buf.String()
	require.True(t, strings.HasPrefix(raw, "\xef\xbb\xbf"), "BOM for Excel")
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\xef\xbb\xbf"))).ReadAll()
	require.NoError(t, err)
	require.Len(t, recs, 6)
	require.Equal(t, CSVHeader, recs[0])
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	// Newest first: the failed call on Mar 3 03:30Z.
	require.Equal(t, "2026-03-03T03:30:00Z", recs[1][col["startedAt"]])
	require.Equal(t, "failed", recs[1][col["status"]])
	require.Equal(t, "Support", recs[1][col["profile"]])
	byOutcome := map[string][]string{}
	for _, r := range recs[1:] {
		byOutcome[r[col["outcome"]]] = r
	}
	agreed := byOutcome["Зөвшөөрсөн"]
	require.NotNil(t, agreed, "label from campaign outcomes")
	require.Equal(t, "Spring", agreed[col["campaign"]])
	require.Equal(t, "Sales", agreed[col["profile"]])
	require.Equal(t, "60", agreed[col["durationSec"]])
	require.Equal(t, "negative", agreed[col["sentiment"]])
	require.Equal(t, "/api/calls/x/recording", agreed[col["recordingUrl"]])
	require.NotNil(t, byOutcome["mystery"], "code when no label")
	// Formula injection is neutralised, contact resolved by name.
	var inbound []string
	for _, r := range recs[1:] {
		if r[col["direction"]] == "inbound" {
			inbound = r
		}
	}
	require.Equal(t, "'=cmd()", inbound[col["summary"]])
	require.Equal(t, "Bat", inbound[col["contact"]])
	require.Equal(t, "+97670", inbound[col["from"]])
	if f.hasCost {
		require.Equal(t, "100", inbound[col["costMnt"]])
		require.Equal(t, "gpt-x", inbound[col["llmModel"]])
	} else {
		require.Equal(t, "0", inbound[col["costMnt"]])
	}
}

func TestExportCSVEmpty(t *testing.T) {
	f := seed(t)
	var buf bytes.Buffer
	n, err := f.store.ExportCSV(f.ctx, uuid.New(), f.from, f.to, &buf)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Len(t, strings.Split(strings.TrimSpace(buf.String()), "\n"), 1, "header only")
}

func TestNormalizeRange(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	from, to, err := NormalizeRange(time.Time{}, time.Time{}, now)
	require.NoError(t, err)
	require.Equal(t, now, to)
	require.Equal(t, now.Add(-30*24*time.Hour), from)
	_, _, err = NormalizeRange(now.Add(-367*24*time.Hour), now, now)
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, _, err = NormalizeRange(now, now, now)
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, _, err = NormalizeRange(now.Add(-366*24*time.Hour), now, now)
	require.NoError(t, err)
}
