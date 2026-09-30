package crm

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var testOutcomes = []domain.CampaignOutcome{
	{Code: "agreed", Label: "Зөвшөөрсөн", Description: "Customer agreed", Terminal: true},
	{Code: "callback", Label: "Дахин залгах", Description: "Asked to call later", Terminal: false},
}

func TestCampaignV2FieldsRoundTrip(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")

	sched := domain.CampaignSchedule{
		Timezone:      "Asia/Ulaanbaatar",
		Weekdays:      []time.Weekday{time.Monday, time.Tuesday, time.Friday},
		StartTime:     "09:00",
		EndTime:       "18:00",
		PacePerMinute: 12,
	}
	targets := makeTargets(4)
	targets[1].Status = domain.TargetSkipped
	targets[1].LastError = "do_not_call"
	targets[3].Status = domain.TargetSkipped
	targets[2].Outcome = "agreed"
	targets[2].OutcomeNote = "Imported with an outcome"
	c := &domain.Campaign{OrgID: org.ID, Name: "V2", Schedule: sched, Outcomes: testOutcomes, DryRunLimit: 3}
	require.NoError(t, s.CreateCampaign(ctx, c, targets))
	require.Equal(t, 4, c.Total, "total counts skipped targets")
	require.Equal(t, 2, c.Skipped)

	got, err := s.GetCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, sched, got.Schedule)
	require.Equal(t, testOutcomes, got.Outcomes)
	require.Equal(t, 3, got.DryRunLimit)
	require.Zero(t, got.DryRunDialed)
	require.Equal(t, 4, got.Total)
	require.Equal(t, 2, got.Skipped)

	all, err := s.ListAllTargets(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, all, 4)
	for i := range all {
		require.Equal(t, targets[i].ID, all[i].ID, "upload order")
	}
	require.Equal(t, domain.TargetSkipped, all[1].Status)
	require.Equal(t, "do_not_call", all[1].LastError)
	require.Equal(t, "agreed", all[2].Outcome)
	require.Equal(t, "Imported with an outcome", all[2].OutcomeNote)

	// Skipped targets are never claimed.
	claimed, err := s.ClaimTargets(ctx, c.ID, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.Equal(t, targets[0].ID, claimed[0].ID)
	require.Equal(t, targets[2].ID, claimed[1].ID)
	require.Equal(t, "agreed", claimed[1].Outcome)

	// Target outcome is written by UpdateTarget.
	tg := claimed[0]
	tg.Status = domain.TargetPending
	tg.Outcome = "callback"
	tg.OutcomeNote = "Маргааш залгана уу"
	require.NoError(t, s.UpdateTarget(ctx, &tg))
	page, _, err := s.ListTargets(ctx, c.ID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, "callback", page[0].Outcome)
	require.Equal(t, "Маргааш залгана уу", page[0].OutcomeNote)

	// UpdateCampaign persists every v2 field; empty schedule/outcomes are
	// stored as {} / [].
	got.Schedule = domain.CampaignSchedule{}
	got.Outcomes = nil
	got.DryRunLimit = 0
	got.DryRunDialed = 0
	got.Skipped = 7
	require.NoError(t, s.UpdateCampaign(ctx, got))
	require.Equal(t, []domain.CampaignOutcome{}, got.Outcomes)
	var schedJSON, outJSON string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT schedule::text, outcomes::text FROM campaigns WHERE id = $1`, c.ID).Scan(&schedJSON, &outJSON))
	require.Equal(t, "{}", schedJSON)
	require.Equal(t, "[]", outJSON)
	again, err := s.GetCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.CampaignSchedule{}, again.Schedule)
	require.True(t, again.Schedule.IsZero())
	require.NotNil(t, again.Outcomes)
	require.Empty(t, again.Outcomes)
	require.Equal(t, 7, again.Skipped)

	again.DryRunLimit = 5
	again.DryRunDialed = 2
	again.Status = domain.CampaignPaused
	require.NoError(t, s.UpdateCampaign(ctx, again))
	list, err := s.ListCampaigns(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 5, list[0].DryRunLimit)
	require.Equal(t, 2, list[0].DryRunDialed)

	// Negative dry-run limits violate the CHECK constraint.
	again.DryRunLimit = -1
	requireErrIs(t, s.UpdateCampaign(ctx, again), domain.ErrInvalid)

	// Recount restores skipped from the targets.
	rc, err := s.RecountCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 2, rc.Skipped)
	require.Equal(t, 4, rc.Total)
	require.Equal(t, 5, rc.DryRunLimit)

	// Running campaigns carry the v2 fields too.
	rc.Status = domain.CampaignRunning
	rc.Schedule = sched
	require.NoError(t, s.UpdateCampaign(ctx, rc))
	running, err := s.ListRunningCampaigns(ctx)
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, sched, running[0].Schedule)
}

func TestCreateCampaignDefaultsV2(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	c := &domain.Campaign{OrgID: org.ID, Name: "Plain"}
	require.NoError(t, s.CreateCampaign(ctx, c, makeTargets(2)))
	require.Zero(t, c.Skipped)
	require.NotNil(t, c.Outcomes)
	got, err := s.GetCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, domain.CampaignSchedule{}, got.Schedule)
	require.Equal(t, []domain.CampaignOutcome{}, got.Outcomes)
	require.Zero(t, got.Skipped)

	// Only a timezone: kept.
	tz := &domain.Campaign{OrgID: org.ID, Name: "TZ", Schedule: domain.CampaignSchedule{Timezone: "UTC"}}
	require.NoError(t, s.CreateCampaign(ctx, tz, nil))
	got, err = s.GetCampaign(ctx, tz.ID)
	require.NoError(t, err)
	require.Equal(t, "UTC", got.Schedule.Timezone)

	all, err := s.ListAllTargets(ctx, uuid.New())
	require.NoError(t, err)
	require.NotNil(t, all)
	require.Empty(t, all)
}

func TestCountTargetsByStatusAndOutcome(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	targets := makeTargets(8)
	targets[7].Status = domain.TargetSkipped
	c := &domain.Campaign{OrgID: org.ID, Name: "Counts", Outcomes: testOutcomes}
	require.NoError(t, s.CreateCampaign(ctx, c, targets))

	byStatus, err := s.CountTargetsByStatus(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, map[domain.CampaignTargetStatus]int{
		domain.TargetPending: 7, domain.TargetCalling: 0, domain.TargetDone: 0,
		domain.TargetFailed: 0, domain.TargetSkipped: 1,
	}, byStatus)
	byOutcome, err := s.CountTargetsByOutcome(ctx, c.ID)
	require.NoError(t, err)
	require.Empty(t, byOutcome)

	claimed, err := s.ClaimTargets(ctx, c.ID, 5)
	require.NoError(t, err)
	require.Len(t, claimed, 5)
	set := func(tg domain.CampaignTarget, st domain.CampaignTargetStatus, outcome string) {
		tg.Status = st
		tg.Outcome = outcome
		require.NoError(t, s.UpdateTarget(ctx, &tg))
	}
	set(claimed[0], domain.TargetDone, "agreed")
	set(claimed[1], domain.TargetDone, "agreed")
	set(claimed[2], domain.TargetPending, "callback")
	set(claimed[3], domain.TargetFailed, "")

	byStatus, err = s.CountTargetsByStatus(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, map[domain.CampaignTargetStatus]int{
		domain.TargetPending: 3, domain.TargetCalling: 1, domain.TargetDone: 2,
		domain.TargetFailed: 1, domain.TargetSkipped: 1,
	}, byStatus)
	byOutcome, err = s.CountTargetsByOutcome(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"agreed": 2, "callback": 1}, byOutcome)

	byStatus, err = s.CountTargetsByStatus(ctx, uuid.New())
	require.NoError(t, err)
	require.Len(t, byStatus, 5)
	for st, n := range byStatus {
		require.Zerof(t, n, "status %s", st)
	}
}

func TestListAllTargetsLarge(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	const n = 1500 // more than the ListTargets page cap
	targets := makeTargets(n)
	c := &domain.Campaign{OrgID: org.ID, Name: "Big"}
	require.NoError(t, s.CreateCampaign(ctx, c, targets))
	all, err := s.ListAllTargets(ctx, c.ID)
	require.NoError(t, err)
	require.Len(t, all, n)
	for i := range all {
		require.Equal(t, fmt.Sprint(i), all[i].Vars["idx"])
	}
}

func TestCallOutcomeRoundTrip(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	call := newCall(t, ctx, s, domain.Call{OrgID: org.ID, Metadata: map[string]any{"targetId": "x"}})
	code, note := callOutcome(call)
	require.Empty(t, code)
	require.Empty(t, note)

	got, err := s.GetCall(ctx, call.ID)
	require.NoError(t, err)
	code, note = callOutcome(got)
	require.Empty(t, code)
	require.Empty(t, note)

	setCallOutcome(got, "callback", "Customer asked to call tomorrow")
	got.Status = domain.StatusCompleted
	require.NoError(t, s.UpdateCall(ctx, got))

	var colCode, colNote, meta string
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT outcome, outcome_note, metadata::text FROM calls WHERE id = $1`, call.ID).Scan(&colCode, &colNote, &meta))
	require.Equal(t, "callback", colCode)
	require.Equal(t, "Customer asked to call tomorrow", colNote)
	require.NotContains(t, meta, "outcome", "outcome lives in its own columns")

	again, err := s.GetCall(ctx, call.ID)
	require.NoError(t, err)
	code, note = callOutcome(again)
	require.Equal(t, "callback", code)
	require.Equal(t, "Customer asked to call tomorrow", note)
	require.Equal(t, "x", again.Metadata["targetId"])

	listed, _, err := s.ListCalls(ctx, domain.CallFilter{OrgID: org.ID})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	code, _ = callOutcome(&listed[0])
	require.Equal(t, "callback", code)

	// Created with an outcome.
	withOutcome := domain.Call{OrgID: org.ID, Status: domain.StatusCompleted}
	setCallOutcome(&withOutcome, "agreed", "")
	created := newCall(t, ctx, s, withOutcome)
	fetched, err := s.GetCall(ctx, created.ID)
	require.NoError(t, err)
	code, note = callOutcome(fetched)
	require.Equal(t, "agreed", code)
	require.Empty(t, note)

	// Stats ignore outcomes.
	st, err := s.Stats(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, 2, st.TotalCalls)
}
