package crm

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestDoNotCallRepository(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "acme")
	other := newOrg(t, ctx, s, "other")
	user := &domain.User{OrgID: org.ID, Email: "admin@acme.mn", Name: "Admin", Role: domain.RoleAdmin, PasswordHash: "x"}
	require.NoError(t, s.CreateUser(ctx, user))

	e := &domain.DoNotCallEntry{OrgID: org.ID, Phone: " +97699110001 ", Reason: "Customer request", CreatedBy: &user.ID}
	require.NoError(t, s.AddDoNotCall(ctx, e))
	require.NotEqual(t, uuid.Nil, e.ID)
	require.Equal(t, "+97699110001", e.Phone)
	require.False(t, e.CreatedAt.IsZero())
	require.Equal(t, user.ID, *e.CreatedBy)

	// Idempotent: the existing entry is returned unchanged.
	dup := &domain.DoNotCallEntry{OrgID: org.ID, Phone: "+97699110001", Reason: "again"}
	created, err := s.InsertDoNotCall(ctx, dup)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, e.ID, dup.ID)
	require.Equal(t, "Customer request", dup.Reason)
	require.NoError(t, s.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: org.ID, Phone: "+97699110001"}))

	requireErrIs(t, s.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: org.ID, Phone: "  "}), domain.ErrInvalid)
	requireErrIs(t, s.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: uuid.New(), Phone: "+1"}), domain.ErrInvalid)

	for i := 2; i <= 5; i++ {
		created, err := s.InsertDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: org.ID,
			Phone: fmt.Sprintf("+9769911000%d", i), Reason: fmt.Sprintf("reason %d", i)})
		require.NoError(t, err)
		require.True(t, created)
	}
	// Same number in another org is independent.
	require.NoError(t, s.AddDoNotCall(ctx, &domain.DoNotCallEntry{OrgID: other.ID, Phone: "+97699110001"}))

	list, total, err := s.ListDoNotCall(ctx, org.ID, "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, list, 5)
	page, total, err := s.ListDoNotCall(ctx, org.ID, "", 2, 4)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, page, 1)
	found, total, err := s.ListDoNotCall(ctx, org.ID, "REASON 3", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "+97699110003", found[0].Phone)
	found, total, err = s.ListDoNotCall(ctx, org.ID, "110001", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, e.ID, found[0].ID)
	found, total, err = s.ListDoNotCall(ctx, org.ID, "100%", 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.NotNil(t, found)

	ok, err := s.IsDoNotCall(ctx, org.ID, "+97699110001")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.IsDoNotCall(ctx, org.ID, "+97699119999")
	require.NoError(t, err)
	require.False(t, ok)

	hits, err := s.FilterDoNotCall(ctx, org.ID, []string{"+97699110001", "+97699110003 ", "+97699119999", ""})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"+97699110001": true, "+97699110003 ": true}, hits)
	hits, err = s.FilterDoNotCall(ctx, org.ID, nil)
	require.NoError(t, err)
	require.Empty(t, hits)
	hits, err = s.FilterDoNotCall(ctx, other.ID, []string{"+97699110001", "+97699110002"})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"+97699110001": true}, hits)

	require.NoError(t, s.RemoveDoNotCall(ctx, org.ID, "+97699110001"))
	requireErrIs(t, s.RemoveDoNotCall(ctx, org.ID, "+97699110001"), domain.ErrNotFound)
	ok, err = s.IsDoNotCall(ctx, org.ID, "+97699110001")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = s.IsDoNotCall(ctx, other.ID, "+97699110001")
	require.NoError(t, err)
	require.True(t, ok, "other org unaffected")
}
