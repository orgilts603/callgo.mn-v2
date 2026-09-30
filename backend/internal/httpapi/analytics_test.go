package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/analytics"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type analyticsEnv struct {
	t     *testing.T
	fake  *fakeAnalytics
	org   uuid.UUID
	deps  AnalyticsDeps
	h     http.Handler
	feats func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
}

func newAnalyticsEnv(t *testing.T, tz string, mutate ...func(*AnalyticsDeps)) *analyticsEnv {
	t.Helper()
	e := &analyticsEnv{t: t, fake: &fakeAnalytics{}, org: uuid.New()}
	e.deps = AnalyticsDeps{
		Store: e.fake,
		Orgs:  &analyticsOrgs{orgs: map[uuid.UUID]*domain.Organization{e.org: {ID: e.org, Timezone: tz}}},
	}
	for _, m := range mutate {
		m(&e.deps)
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("X-Test-NoAuth") != "" {
					next.ServeHTTP(w, req)
					return
				}
				next.ServeHTTP(w, req.WithContext(auth.WithClaims(req.Context(),
					auth.Claims{UserID: uuid.New(), OrgID: e.org, Role: domain.RoleOperator})))
			})
		})
		mountAnalytics(r, e.deps, zerolog.Nop())
	})
	e.h = r
	return e
}

func (e *analyticsEnv) get(path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/analytics"+path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Code
}

func TestAnalyticsOverviewJSONShape(t *testing.T) {
	e := newAnalyticsEnv(t, "Asia/Ulaanbaatar")
	e.fake.overview = &analytics.Overview{
		Calls: 10, Answered: 8, AnswerRate: 0.8, AvgDurationSec: 61.5, TotalMinutes: 9.5, CostMnt: 400, CostPerCallMnt: 40,
		Sentiment:   analytics.SentimentCounts{Positive: 3, Neutral: 2, Negative: 1},
		Outcomes:    []analytics.OutcomeCount{{Code: "agreed", Label: "Yes", Count: 4}},
		ByDirection: analytics.DirectionCounts{Inbound: 6, Outbound: 4},
	}
	rec := e.get("/overview?from=2026-03-01&to=2026-03-07")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	for _, k := range []string{"calls", "answered", "answerRate", "avgDurationSec", "totalMinutes", "costMnt", "costPerCallMnt", "sentiment", "outcomes", "byDirection"} {
		require.Contains(t, got, k)
	}
	require.Equal(t, map[string]any{"positive": 3.0, "neutral": 2.0, "negative": 1.0}, got["sentiment"])
	require.Equal(t, map[string]any{"inbound": 6.0, "outbound": 4.0}, got["byDirection"])
	require.Equal(t, []any{map[string]any{"code": "agreed", "label": "Yes", "count": 4.0}}, got["outcomes"])
}

func TestAnalyticsRangeParsing(t *testing.T) {
	e := newAnalyticsEnv(t, "Asia/Ulaanbaatar")
	ubn := analytics.Location("Asia/Ulaanbaatar")

	// Date-only bounds are org-local; `to` covers the whole day.
	require.Equal(t, http.StatusOK, e.get("/overview?from=2026-03-01&to=2026-03-07").Code)
	c := e.fake.last()
	require.Equal(t, e.org, c.Org)
	require.Equal(t, "Asia/Ulaanbaatar", c.TZ)
	require.True(t, c.From.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, ubn)))
	require.True(t, c.To.Equal(time.Date(2026, 3, 8, 0, 0, 0, 0, ubn)))

	// RFC3339 is taken as-is.
	require.Equal(t, http.StatusOK, e.get("/overview?from=2026-03-01T00:00:00Z&to=2026-03-02T12:00:00Z").Code)
	c = e.fake.last()
	require.True(t, c.From.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)))
	require.True(t, c.To.Equal(time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)))

	// Defaults: last 30 days ending now.
	before := time.Now()
	require.Equal(t, http.StatusOK, e.get("/overview").Code)
	c = e.fake.last()
	require.WithinDuration(t, before, c.To, 5*time.Second)
	require.WithinDuration(t, c.To.Add(-30*24*time.Hour), c.From, time.Second)

	for name, q := range map[string]string{
		"bad from":     "?from=yesterday",
		"bad to":       "?to=2026-13-40",
		"reversed":     "?from=2026-03-05&to=2026-03-01",
		"too wide":     "?from=2024-01-01&to=2026-01-01",
		"same instant": "?from=2026-03-01T00:00:00Z&to=2026-03-01T00:00:00Z",
	} {
		n := e.fake.count()
		rec := e.get("/overview" + q)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
		require.Equal(t, "invalid", errCode(t, rec), name)
		require.Equal(t, n, e.fake.count(), "%s must not reach the store", name)
	}
	// 366 days is allowed.
	require.Equal(t, http.StatusOK, e.get("/overview?from=2025-03-01&to=2026-02-28").Code)
}

func TestAnalyticsTimezoneFallback(t *testing.T) {
	e := newAnalyticsEnv(t, "Not/AZone")
	require.Equal(t, http.StatusOK, e.get("/heatmap").Code)
	require.Equal(t, analytics.DefaultTimezone, e.fake.last().TZ)

	e = newAnalyticsEnv(t, "America/New_York")
	require.Equal(t, http.StatusOK, e.get("/heatmap").Code)
	require.Equal(t, "America/New_York", e.fake.last().TZ)

	// Unknown org row → default tz, not an error.
	e = newAnalyticsEnv(t, "", func(d *AnalyticsDeps) { d.Orgs = &analyticsOrgs{} })
	require.Equal(t, http.StatusOK, e.get("/heatmap").Code)
	require.Equal(t, analytics.DefaultTimezone, e.fake.last().TZ)

	e = newAnalyticsEnv(t, "", func(d *AnalyticsDeps) { d.Orgs = &analyticsOrgs{err: errors.New("db down")} })
	rec := e.get("/heatmap")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "internal", errCode(t, rec))
	require.NotContains(t, rec.Body.String(), "db down")
}

func TestAnalyticsTimeseriesBucket(t *testing.T) {
	e := newAnalyticsEnv(t, "UTC")
	rec := e.get("/timeseries")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "day", e.fake.last().Bucket)
	require.JSONEq(t, `{"items":[]}`, rec.Body.String(), "nil result is an empty array")

	require.Equal(t, http.StatusOK, e.get("/timeseries?bucket=hour").Code)
	require.Equal(t, "hour", e.fake.last().Bucket)

	n := e.fake.count()
	rec = e.get("/timeseries?bucket=week")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid", errCode(t, rec))
	require.Equal(t, n, e.fake.count())

	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	e.fake.points = []analytics.Point{{TS: ts, Calls: 2, Answered: 1, Minutes: 1.5, CostMnt: 20}}
	rec = e.get("/timeseries")
	require.JSONEq(t, `{"items":[{"ts":"2026-03-01T00:00:00Z","calls":2,"answered":1,"minutes":1.5,"costMnt":20}]}`, rec.Body.String())
}

func TestAnalyticsListEndpoints(t *testing.T) {
	e := newAnalyticsEnv(t, "UTC")
	pid, cid := uuid.New(), uuid.New()

	rec := e.get("/heatmap")
	require.JSONEq(t, `{"cells":[]}`, rec.Body.String())
	e.fake.cells = []analytics.HeatmapCell{{Weekday: 1, Hour: 10, Calls: 3, AnswerRate: 0.5}}
	require.JSONEq(t, `{"cells":[{"weekday":1,"hour":10,"calls":3,"answerRate":0.5}]}`, e.get("/heatmap").Body.String())

	require.JSONEq(t, `{"items":[]}`, e.get("/profiles").Body.String())
	e.fake.profiles = []analytics.ProfileStat{{ProfileID: pid, Name: "Sales", Calls: 3, AnswerRate: 1, AvgDurationSec: 30, PositiveRate: 0.5, CostMnt: 9,
		Outcomes: map[string]int{"agreed": 2}}}
	require.JSONEq(t, `{"items":[{"profileId":"`+pid.String()+`","name":"Sales","calls":3,"answerRate":1,"avgDurationSec":30,"positiveRate":0.5,"costMnt":9,"outcomes":{"agreed":2}}]}`,
		e.get("/profiles").Body.String())

	require.JSONEq(t, `{"items":[]}`, e.get("/campaigns").Body.String())
	e.fake.camps = []analytics.CampaignStat{{CampaignID: cid, Name: "Spring", Total: 4, Done: 1, Failed: 1, Skipped: 1, Outcomes: map[string]int{}, Minutes: 1.5, CostMnt: 70}}
	require.JSONEq(t, `{"items":[{"campaignId":"`+cid.String()+`","name":"Spring","total":4,"done":1,"failed":1,"skipped":1,"outcomes":{},"minutes":1.5,"costMnt":70}]}`,
		e.get("/campaigns").Body.String())
}

func TestAnalyticsFeatureGate(t *testing.T) {
	var asked []string
	allowed := false
	e := newAnalyticsEnv(t, "UTC", func(d *AnalyticsDeps) {
		d.HasFeature = func(_ context.Context, _ uuid.UUID, feature string) (bool, error) {
			asked = append(asked, feature)
			return allowed, nil
		}
	})
	for _, p := range []string{"/overview", "/timeseries", "/heatmap", "/profiles", "/campaigns", "/export.csv"} {
		rec := e.get(p)
		require.Equal(t, http.StatusForbidden, rec.Code, p)
		require.Equal(t, "feature_unavailable", errCode(t, rec), p)
	}
	require.Zero(t, e.fake.count(), "gated requests never reach the store")
	require.Equal(t, "analytics", asked[0])

	allowed = true
	require.Equal(t, http.StatusOK, e.get("/overview").Code)
	require.Equal(t, http.StatusOK, e.get("/export.csv").Code)

	e2 := newAnalyticsEnv(t, "UTC", func(d *AnalyticsDeps) {
		d.HasFeature = func(context.Context, uuid.UUID, string) (bool, error) { return false, errors.New("boom") }
	})
	rec := e2.get("/overview")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "internal", errCode(t, rec))

	// nil HasFeature = allowed (covered by every other test).
}

func TestAnalyticsRequiresClaims(t *testing.T) {
	e := newAnalyticsEnv(t, "UTC")
	rec := e.get("/overview", "X-Test-NoAuth", "1")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, e.fake.count())
}

func TestAnalyticsStoreErrors(t *testing.T) {
	e := newAnalyticsEnv(t, "UTC")
	e.fake.err = errors.New("pq: secret detail")
	for _, p := range []string{"/overview", "/timeseries", "/heatmap", "/profiles", "/campaigns", "/export.csv"} {
		rec := e.get(p)
		require.Equal(t, http.StatusInternalServerError, rec.Code, p)
		require.Equal(t, "internal", errCode(t, rec), p)
		require.NotContains(t, rec.Body.String(), "secret detail")
	}
	e.fake.err = domain.ErrInvalid
	require.Equal(t, http.StatusBadRequest, e.get("/overview").Code)
}

func TestAnalyticsExportCSV(t *testing.T) {
	e := newAnalyticsEnv(t, "Asia/Ulaanbaatar")
	e.fake.csv = "startedAt,direction\n2026-03-01T00:00:00Z,inbound\n"
	rec := e.get("/export.csv?from=2026-03-01&to=2026-03-07")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="callgo-calls-20260301_20260307.csv"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, e.fake.csv, rec.Body.String())
	require.Equal(t, "export", e.fake.last().Method)

	// Failure after output started keeps the 200 and truncated body.
	e.fake.csvErrAfter = true
	e.fake.err = errors.New("stream broke")
	rec = e.get("/export.csv")
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Body.String(), "startedAt"))

	// Failure before any output is a JSON error, not an empty CSV.
	e.fake.csvErrAfter = false
	rec = e.get("/export.csv")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
}

func TestAnalyticsStoreNotConfigured(t *testing.T) {
	e := newAnalyticsEnv(t, "UTC", func(d *AnalyticsDeps) { d.Store = nil })
	require.Equal(t, http.StatusInternalServerError, e.get("/overview").Code)
}
