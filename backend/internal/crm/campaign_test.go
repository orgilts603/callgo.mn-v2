package crm

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func makeTargets(n int) []domain.CampaignTarget {
	ts := make([]domain.CampaignTarget, n)
	for i := range ts {
		ts[i] = domain.CampaignTarget{Phone: fmt.Sprintf("+976880%05d", i), Name: fmt.Sprintf("T%d", i),
			Vars: map[string]string{"idx": fmt.Sprint(i)}}
	}
	return ts
}

func TestCampaignLifecycle(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	num := &domain.SIPNumber{OrgID: org.ID, Number: "+97677001234"}
	require.NoError(t, s.CreateSIPNumber(ctx, num))
	contact := &domain.Contact{OrgID: org.ID, Phone: "+97688000000"}
	require.NoError(t, s.UpsertContact(ctx, contact))

	targets := makeTargets(5)
	targets[0].ContactID = &contact.ID
	c := &domain.Campaign{OrgID: org.ID, Name: "Promo", SIPNumberID: &num.ID, Script: "Хямдралын тухай мэдэгд"}
	require.NoError(t, s.CreateCampaign(ctx, c, targets))
	require.NotEqual(t, uuid.Nil, c.ID)
	require.Equal(t, 5, c.Total)
	require.Equal(t, domain.CampaignDraft, c.Status)
	require.Equal(t, 2, c.Concurrency)
	require.Equal(t, 2, c.MaxAttempts)
	for _, tg := range targets {
		require.NotEqual(t, uuid.Nil, tg.ID)
		require.Equal(t, c.ID, tg.CampaignID)
		require.Equal(t, domain.TargetPending, tg.Status)
	}

	got, err := s.GetCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 5, got.Total)
	require.Equal(t, num.ID, *got.SIPNumberID)
	require.Equal(t, "Хямдралын тухай мэдэгд", got.Script)

	list, total, err := s.ListTargets(ctx, c.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	for i, tg := range list {
		require.Equal(t, targets[i].ID, tg.ID, "upload order preserved")
		require.Equal(t, fmt.Sprint(i), tg.Vars["idx"])
	}
	require.Equal(t, contact.ID, *list[0].ContactID)
	page, total, err := s.ListTargets(ctx, c.ID, 2, 3)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Equal(t, []uuid.UUID{targets[3].ID, targets[4].ID}, []uuid.UUID{page[0].ID, page[1].ID})

	running, err := s.ListRunningCampaigns(ctx)
	require.NoError(t, err)
	require.Empty(t, running)
	c.Status = domain.CampaignRunning
	c.Concurrency = 5
	require.NoError(t, s.UpdateCampaign(ctx, c))
	running, err = s.ListRunningCampaigns(ctx)
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, 5, running[0].Concurrency)

	// Claim two, then fail one with a retry in the future.
	claimed, err := s.ClaimTargets(ctx, c.ID, 2)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.Equal(t, targets[0].ID, claimed[0].ID)
	for _, tg := range claimed {
		require.Equal(t, domain.TargetCalling, tg.Status)
		require.Equal(t, 1, tg.Attempts)
	}
	active, err := s.CountActiveTargets(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 2, active)

	call := newCall(t, ctx, s, domain.Call{OrgID: org.ID, Direction: "outbound", CampaignID: &c.ID})
	retry := claimed[0]
	retry.Status = domain.TargetPending
	retry.LastError = "no_answer"
	retry.CallID = &call.ID
	retry.NextTryAt = ptr(time.Now().Add(time.Hour))
	require.NoError(t, s.UpdateTarget(ctx, &retry))
	done := claimed[1]
	done.Status = domain.TargetDone
	require.NoError(t, s.UpdateTarget(ctx, &done))

	pending, err := s.CountPendingTargets(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 4, pending)

	// The retry is not due: remaining claims skip it.
	claimed, err = s.ClaimTargets(ctx, c.ID, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 3)
	for _, tg := range claimed {
		require.NotEqual(t, retry.ID, tg.ID)
	}
	claimed, err = s.ClaimTargets(ctx, c.ID, 10)
	require.NoError(t, err)
	require.Empty(t, claimed)

	// Once due, it is claimable again with attempts incremented.
	retry.NextTryAt = ptr(time.Now().Add(-time.Second))
	require.NoError(t, s.UpdateTarget(ctx, &retry))
	claimed, err = s.ClaimTargets(ctx, c.ID, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, retry.ID, claimed[0].ID)
	require.Equal(t, 2, claimed[0].Attempts)
	require.Equal(t, "no_answer", claimed[0].LastError)
	require.Equal(t, call.ID, *claimed[0].CallID)

	claimed[0].Status = domain.TargetFailed
	require.NoError(t, s.UpdateTarget(ctx, &claimed[0]))
	rc, err := s.RecountCampaign(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, 5, rc.Total)
	require.Equal(t, 1, rc.Completed)
	require.Equal(t, 1, rc.Failed)

	none, err := s.ClaimTargets(ctx, c.ID, 0)
	require.NoError(t, err)
	require.Empty(t, none)
	requireErrIs(t, s.UpdateTarget(ctx, &domain.CampaignTarget{ID: uuid.New(), Status: "pending"}), domain.ErrNotFound)
	requireErrIs(t, s.UpdateTarget(ctx, &domain.CampaignTarget{ID: retry.ID, Status: "bogus"}), domain.ErrInvalid)

	camps, err := s.ListCampaigns(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, camps, 1)

	// Deleting keeps the calls but drops targets.
	require.NoError(t, s.DeleteCampaign(ctx, c.ID))
	requireErrIs(t, s.DeleteCampaign(ctx, c.ID), domain.ErrNotFound)
	_, err = s.GetCampaign(ctx, c.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	gotCall, err := s.GetCall(ctx, call.ID)
	require.NoError(t, err)
	require.Nil(t, gotCall.CampaignID)
	_, total, err = s.ListTargets(ctx, c.ID, 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	requireErrIs(t, s.UpdateCampaign(ctx, &domain.Campaign{ID: uuid.New(), Name: "x"}), domain.ErrNotFound)
	_, err = s.RecountCampaign(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)
}

func TestCreateCampaignIsAtomic(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")

	targets := makeTargets(3)
	targets[2].ContactID = ptr(uuid.New()) // FK violation aborts everything
	err := s.CreateCampaign(ctx, &domain.Campaign{OrgID: org.ID, Name: "Broken"}, targets)
	requireErrIs(t, err, domain.ErrInvalid)
	camps, err := s.ListCampaigns(ctx, org.ID)
	require.NoError(t, err)
	require.Empty(t, camps)

	empty := &domain.Campaign{OrgID: org.ID, Name: "Empty", Concurrency: 3, MaxAttempts: 1}
	require.NoError(t, s.CreateCampaign(ctx, empty, nil))
	require.Equal(t, 0, empty.Total)
}

func TestClaimTargetsConcurrent(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	const n = 500
	c := &domain.Campaign{OrgID: org.ID, Name: "Big", Status: domain.CampaignRunning}
	require.NoError(t, s.CreateCampaign(ctx, c, makeTargets(n)))

	const workers = 8
	var (
		mu   sync.Mutex
		seen = make(map[uuid.UUID]int, n)
		wg   sync.WaitGroup
	)
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := s.ClaimTargets(ctx, c.ID, 7)
				if err != nil {
					errs <- err
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, tg := range got {
					seen[tg.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, seen, n, "every target claimed")
	for id, k := range seen {
		require.Equalf(t, 1, k, "target %s claimed %d times", id, k)
	}
	active, err := s.CountActiveTargets(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, n, active)
}
