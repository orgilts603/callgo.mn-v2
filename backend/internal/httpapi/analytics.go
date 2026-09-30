package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/analytics"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// AnalyticsDeps wires the /api/analytics routes.
type AnalyticsDeps struct {
	// Store answers the queries; *analytics.Store implements it.
	Store analytics.Querier
	// Orgs supplies the org timezone (heatmap, day buckets, date-only bounds).
	Orgs domain.OrgRepository
	// HasFeature gates the routes on the `analytics` plan feature. nil = allowed.
	HasFeature func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
}

// featureAnalytics is the plan feature that unlocks the routes.
const featureAnalytics = "analytics"

type analyticsAPI struct {
	AnalyticsDeps
	log zerolog.Logger
	now func() time.Time
}

// mountAnalytics registers the analytics routes on r (already authenticated):
//
//	GET /analytics/{overview,timeseries,heatmap,profiles,campaigns,export.csv}
func mountAnalytics(r chi.Router, d AnalyticsDeps, log zerolog.Logger) {
	a := &analyticsAPI{AnalyticsDeps: d, log: log, now: time.Now}
	r.Route("/analytics", func(r chi.Router) {
		r.Get("/overview", a.overview)
		r.Get("/timeseries", a.timeseries)
		r.Get("/heatmap", a.heatmap)
		r.Get("/profiles", a.profiles)
		r.Get("/campaigns", a.campaigns)
		r.Get("/export.csv", a.exportCSV)
	})
}

// query is the parsed common request context.
type analyticsQuery struct {
	org      uuid.UUID
	from, to time.Time
	tz       string
	loc      *time.Location
}

// prepare enforces the feature gate and parses org, timezone and range. It
// writes the error response itself and returns false on failure.
func (a *analyticsAPI) prepare(w http.ResponseWriter, r *http.Request) (analyticsQuery, bool) {
	var q analyticsQuery
	c := claimsOf(r)
	if c.OrgID == uuid.Nil {
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return q, false
	}
	q.org = c.OrgID
	if a.Store == nil {
		a.fail(w, r, errNotConfigured("analytics"))
		return q, false
	}
	if a.HasFeature != nil {
		ok, err := a.HasFeature(r.Context(), q.org, featureAnalytics)
		if err != nil {
			a.fail(w, r, fmt.Errorf("check feature %s: %w", featureAnalytics, err))
			return q, false
		}
		if !ok {
			auth.WriteError(w, http.StatusForbidden, "feature_unavailable", "analytics is not available on your plan")
			return q, false
		}
	}

	tz := analytics.DefaultTimezone
	if a.Orgs != nil {
		org, err := a.Orgs.GetOrg(r.Context(), q.org)
		switch {
		case err == nil:
			if org.Timezone != "" {
				tz = org.Timezone
			}
		case errors.Is(err, domain.ErrNotFound):
		default:
			a.fail(w, r, fmt.Errorf("load org: %w", err))
			return q, false
		}
	}
	q.loc = analytics.Location(tz)
	q.tz = q.loc.String()

	from, to, err := parseAnalyticsRange(r, q.loc, a.now())
	if err != nil {
		a.fail(w, r, err)
		return q, false
	}
	q.from, q.to = from, to
	return q, true
}

// parseAnalyticsRange reads from/to (RFC3339 or YYYY-MM-DD in loc). A
// date-only `to` is inclusive of that whole day. Defaults and the 366-day cap
// are applied by analytics.NormalizeRange.
func parseAnalyticsRange(r *http.Request, loc *time.Location, now time.Time) (time.Time, time.Time, error) {
	var from, to time.Time
	var err error
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		if from, _, err = parseAnalyticsTime(v, loc); err != nil {
			return from, to, errInvalid("from: %v", err)
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		var dateOnly bool
		if to, dateOnly, err = parseAnalyticsTime(v, loc); err != nil {
			return from, to, errInvalid("to: %v", err)
		}
		if dateOnly {
			to = time.Date(to.Year(), to.Month(), to.Day()+1, 0, 0, 0, 0, loc)
		}
	}
	from, to, err = analytics.NormalizeRange(from, to, now)
	if err != nil {
		return from, to, errInvalid("%s", strings.TrimPrefix(err.Error(), domain.ErrInvalid.Error()+": "))
	}
	return from, to, nil
}

func parseAnalyticsTime(v string, loc *time.Location) (t time.Time, dateOnly bool, err error) {
	if t, err = time.Parse(time.RFC3339, v); err == nil {
		return t, false, nil
	}
	if t, err = time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, errors.New("must be RFC3339 or YYYY-MM-DD")
}

// fail maps err onto the error envelope.
func (a *analyticsAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		auth.WriteError(w, ae.status, ae.code, ae.message)
	case errors.Is(err, domain.ErrInvalid):
		auth.WriteError(w, http.StatusBadRequest, "invalid", strings.TrimPrefix(err.Error(), domain.ErrInvalid.Error()+": "))
	default:
		a.log.Error().Err(err).Str("path", r.URL.Path).Str("reqId", middleware.GetReqID(r.Context())).Msg("analytics request failed")
		auth.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func (a *analyticsAPI) overview(w http.ResponseWriter, r *http.Request) {
	q, ok := a.prepare(w, r)
	if !ok {
		return
	}
	o, err := a.Store.Overview(r.Context(), q.org, q.from, q.to, q.tz)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (a *analyticsAPI) timeseries(w http.ResponseWriter, r *http.Request) {
	q, ok := a.prepare(w, r)
	if !ok {
		return
	}
	bucket := strings.TrimSpace(r.URL.Query().Get("bucket"))
	switch bucket {
	case "":
		bucket = analytics.BucketDay
	case analytics.BucketDay, analytics.BucketHour:
	default:
		a.fail(w, r, errInvalid("bucket must be hour or day"))
		return
	}
	pts, err := a.Store.Timeseries(r.Context(), q.org, q.from, q.to, bucket, q.tz)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if pts == nil {
		pts = []analytics.Point{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": pts})
}

func (a *analyticsAPI) heatmap(w http.ResponseWriter, r *http.Request) {
	q, ok := a.prepare(w, r)
	if !ok {
		return
	}
	cells, err := a.Store.Heatmap(r.Context(), q.org, q.from, q.to, q.tz)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if cells == nil {
		cells = []analytics.HeatmapCell{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cells": cells})
}

func (a *analyticsAPI) profiles(w http.ResponseWriter, r *http.Request) {
	q, ok := a.prepare(w, r)
	if !ok {
		return
	}
	items, err := a.Store.Profiles(r.Context(), q.org, q.from, q.to)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []analytics.ProfileStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *analyticsAPI) campaigns(w http.ResponseWriter, r *http.Request) {
	q, ok := a.prepare(w, r)
	if !ok {
		return
	}
	items, err := a.Store.Campaigns(r.Context(), q.org, q.from, q.to)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []analytics.CampaignStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// lazyCSV sends the CSV response headers only once the first byte is written,
// so a failure before any output can still be reported as a JSON error.
type lazyCSV struct {
	w       http.ResponseWriter
	name    string
	started bool
}

func (l *lazyCSV) Write(p []byte) (int, error) {
	if !l.started {
		l.started = true
		h := l.w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+l.name+`"`)
		h.Set("Cache-Control", "no-store")
		l.w.WriteHeader(http.StatusOK)
	}
	return l.w.Write(p)
}

func (a *analyticsAPI) exportCSV(w http.ResponseWriter, r *http.Request) {
	q, ok := a.prepare(w, r)
	if !ok {
		return
	}
	name := fmt.Sprintf("callgo-calls-%s_%s.csv", q.from.In(q.loc).Format("20060102"), q.to.Add(-time.Second).In(q.loc).Format("20060102"))
	out := &lazyCSV{w: w, name: name}
	if _, err := a.Store.ExportCSV(r.Context(), q.org, q.from, q.to, out); err != nil {
		if !out.started {
			a.fail(w, r, err)
			return
		}
		// Headers are gone; the truncated body is all we can signal.
		a.log.Error().Err(err).Str("org", q.org.String()).Msg("analytics csv export aborted")
	}
}
