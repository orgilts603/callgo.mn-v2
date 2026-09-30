package httpapi

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/analytics"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// analyticsCall records the arguments of one Querier call.
type analyticsCall struct {
	Method   string
	Org      uuid.UUID
	From, To time.Time
	TZ       string
	Bucket   string
}

// fakeAnalytics is an in-memory analytics.Querier.
type fakeAnalytics struct {
	mu    sync.Mutex
	calls []analyticsCall

	overview *analytics.Overview
	points   []analytics.Point
	cells    []analytics.HeatmapCell
	profiles []analytics.ProfileStat
	camps    []analytics.CampaignStat
	csv      string
	err      error
	// csvErrAfter makes ExportCSV fail after writing csv.
	csvErrAfter bool
}

func (f *fakeAnalytics) record(c analyticsCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeAnalytics) last() analyticsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return analyticsCall{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeAnalytics) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeAnalytics) Overview(_ context.Context, org uuid.UUID, from, to time.Time, tz string) (*analytics.Overview, error) {
	f.record(analyticsCall{Method: "overview", Org: org, From: from, To: to, TZ: tz})
	if f.err != nil {
		return nil, f.err
	}
	if f.overview == nil {
		return &analytics.Overview{Outcomes: []analytics.OutcomeCount{}}, nil
	}
	return f.overview, nil
}

func (f *fakeAnalytics) Timeseries(_ context.Context, org uuid.UUID, from, to time.Time, bucket, tz string) ([]analytics.Point, error) {
	f.record(analyticsCall{Method: "timeseries", Org: org, From: from, To: to, TZ: tz, Bucket: bucket})
	return f.points, f.err
}

func (f *fakeAnalytics) Heatmap(_ context.Context, org uuid.UUID, from, to time.Time, tz string) ([]analytics.HeatmapCell, error) {
	f.record(analyticsCall{Method: "heatmap", Org: org, From: from, To: to, TZ: tz})
	return f.cells, f.err
}

func (f *fakeAnalytics) Profiles(_ context.Context, org uuid.UUID, from, to time.Time) ([]analytics.ProfileStat, error) {
	f.record(analyticsCall{Method: "profiles", Org: org, From: from, To: to})
	return f.profiles, f.err
}

func (f *fakeAnalytics) Campaigns(_ context.Context, org uuid.UUID, from, to time.Time) ([]analytics.CampaignStat, error) {
	f.record(analyticsCall{Method: "campaigns", Org: org, From: from, To: to})
	return f.camps, f.err
}

func (f *fakeAnalytics) ExportCSV(_ context.Context, org uuid.UUID, from, to time.Time, w io.Writer) (int, error) {
	f.record(analyticsCall{Method: "export", Org: org, From: from, To: to})
	if f.err != nil && !f.csvErrAfter {
		return 0, f.err
	}
	if _, err := io.WriteString(w, f.csv); err != nil {
		return 0, err
	}
	if f.csvErrAfter {
		return 1, f.err
	}
	return 1, nil
}

// analyticsOrgs is a domain.OrgRepository that only knows GetOrg.
type analyticsOrgs struct {
	domain.OrgRepository
	orgs map[uuid.UUID]*domain.Organization
	err  error
}

func (o *analyticsOrgs) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	if o.err != nil {
		return nil, o.err
	}
	org, ok := o.orgs[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return org, nil
}
