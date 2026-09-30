package crm

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestLexicon(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	other := newOrg(t, ctx, s, "other")
	user := &domain.User{OrgID: org.ID, Email: "a@b.c"}
	require.NoError(t, s.CreateUser(ctx, user))
	call := newCall(t, ctx, s, domain.Call{OrgID: org.ID})
	turn := &domain.TranscriptTurn{CallID: call.ID, Speaker: domain.SpeakerCustomer, Text: "колл го"}
	require.NoError(t, s.AddTurn(ctx, turn))

	a := &domain.LexiconCorrection{OrgID: org.ID, Wrong: " Колл Го ", Correct: "CallGo", SourceTurn: &turn.ID,
		CreatedBy: &user.ID}
	require.NoError(t, s.AddCorrection(ctx, a))
	require.NotEqual(t, uuid.Nil, a.ID)
	require.Equal(t, domain.ScopeSTT, a.Scope)
	require.Equal(t, "Колл Го", a.Wrong)

	// Case-insensitive duplicate in the same org conflicts; other org is fine.
	requireErrIs(t, s.AddCorrection(ctx, &domain.LexiconCorrection{OrgID: org.ID, Wrong: "колл го", Correct: "x"}),
		domain.ErrConflict)
	require.NoError(t, s.AddCorrection(ctx, &domain.LexiconCorrection{OrgID: other.ID, Wrong: "колл го", Correct: "x"}))
	requireErrIs(t, s.AddCorrection(ctx, &domain.LexiconCorrection{OrgID: org.ID, Wrong: "y", Correct: "x", Scope: "all"}),
		domain.ErrInvalid)

	b := &domain.LexiconCorrection{OrgID: org.ID, Wrong: "хаан банк", Correct: "Хаан Банк", Phonetic: "haan bank",
		Scope: domain.ScopeBoth}
	require.NoError(t, s.AddCorrection(ctx, b))

	list, err := s.ListCorrections(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, b.ID, list[0].ID, "newest first")
	require.Equal(t, turn.ID, *list[1].SourceTurn)
	require.Equal(t, user.ID, *list[1].CreatedBy)

	b.Wrong = "КОЛЛ ГО"
	requireErrIs(t, s.UpdateCorrection(ctx, b), domain.ErrConflict)
	b.Wrong = "хаан банкны"
	b.Scope = domain.ScopeTTS
	require.NoError(t, s.UpdateCorrection(ctx, b))
	require.Equal(t, org.ID, b.OrgID)
	got, err := s.GetCorrection(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, "хаан банкны", got.Wrong)
	require.Equal(t, domain.ScopeTTS, got.Scope)
	require.Equal(t, "haan bank", got.Phonetic)

	require.NoError(t, s.IncrementHits(ctx, []uuid.UUID{a.ID, b.ID, a.ID, uuid.New()}))
	require.NoError(t, s.IncrementHits(ctx, []uuid.UUID{a.ID}))
	require.NoError(t, s.IncrementHits(ctx, nil))
	got, err = s.GetCorrection(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 3, got.HitCount)
	got, err = s.GetCorrection(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.HitCount)

	// Deleting the source turn's call keeps the correction.
	_, err = testPool.Exec(ctx, `DELETE FROM calls WHERE id = $1`, call.ID)
	require.NoError(t, err)
	got, err = s.GetCorrection(ctx, a.ID)
	require.NoError(t, err)
	require.Nil(t, got.SourceTurn)

	require.NoError(t, s.DeleteCorrection(ctx, a.ID))
	requireErrIs(t, s.DeleteCorrection(ctx, a.ID), domain.ErrNotFound)
	requireErrIs(t, s.UpdateCorrection(ctx, &domain.LexiconCorrection{ID: uuid.New(), Wrong: "q", Correct: "w"}),
		domain.ErrNotFound)
	_, err = s.GetCorrection(ctx, a.ID)
	requireErrIs(t, err, domain.ErrNotFound)
}
