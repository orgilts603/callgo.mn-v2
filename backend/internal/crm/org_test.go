package crm

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestOrgsAndUsers(t *testing.T) {
	ctx, s := setup(t)

	o1, err := s.EnsureDefaultOrg(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultOrgSlug, o1.Slug)
	require.Equal(t, DefaultOrgName, o1.Name)
	o2, err := s.EnsureDefaultOrg(ctx)
	require.NoError(t, err)
	require.Equal(t, o1.ID, o2.ID, "EnsureDefaultOrg is get-or-create")

	got, err := s.GetOrg(ctx, o1.ID)
	require.NoError(t, err)
	require.Equal(t, o1.Slug, got.Slug)
	got, err = s.GetOrgBySlug(ctx, "demo")
	require.NoError(t, err)
	require.Equal(t, o1.ID, got.ID)

	_, err = s.GetOrg(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)
	_, err = s.GetOrgBySlug(ctx, "nope")
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.CreateOrg(ctx, &domain.Organization{Name: "dup", Slug: "demo"}), domain.ErrConflict)

	u := &domain.User{OrgID: o1.ID, Email: " Admin@CallGo.mn ", Name: "Admin", Role: domain.RoleOwner, PasswordHash: "hash"}
	require.NoError(t, s.CreateUser(ctx, u))
	require.NotEqual(t, uuid.Nil, u.ID)
	require.False(t, u.CreatedAt.IsZero())
	require.Equal(t, "Admin@CallGo.mn", u.Email)

	requireErrIs(t, s.CreateUser(ctx, &domain.User{OrgID: o1.ID, Email: "admin@callgo.mn"}), domain.ErrConflict)
	requireErrIs(t, s.CreateUser(ctx, &domain.User{OrgID: uuid.New(), Email: "x@y.z"}), domain.ErrInvalid)

	byEmail, err := s.GetUserByEmail(ctx, "ADMIN@callgo.MN")
	require.NoError(t, err)
	require.Equal(t, u.ID, byEmail.ID)
	require.Equal(t, "hash", byEmail.PasswordHash)
	require.Equal(t, domain.RoleOwner, byEmail.Role)

	op := &domain.User{OrgID: o1.ID, Email: "op@callgo.mn", Name: "Op"}
	require.NoError(t, s.CreateUser(ctx, op))
	require.Equal(t, domain.RoleOperator, op.Role, "role defaults to operator")

	byID, err := s.GetUser(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, "op@callgo.mn", byID.Email)
	_, err = s.GetUser(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)
	_, err = s.GetUserByEmail(ctx, "missing@callgo.mn")
	requireErrIs(t, err, domain.ErrNotFound)

	users, err := s.ListUsers(ctx, o1.ID)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, u.ID, users[0].ID)

	other := newOrg(t, ctx, s, "other")
	users, err = s.ListUsers(ctx, other.ID)
	require.NoError(t, err)
	require.NotNil(t, users)
	require.Empty(t, users)

	n, err := s.CountAllUsers(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

func TestSeedDemoIdempotent(t *testing.T) {
	ctx, s := setup(t)

	org, err := s.SeedDemo(ctx, "admin@callgo.mn", "hash-1")
	require.NoError(t, err)
	require.Equal(t, "demo", org.Slug)

	again, err := s.SeedDemo(ctx, "admin@callgo.mn", "hash-2")
	require.NoError(t, err)
	require.Equal(t, org.ID, again.ID)

	u, err := s.GetUserByEmail(ctx, "admin@callgo.mn")
	require.NoError(t, err)
	require.Equal(t, domain.RoleOwner, u.Role)
	require.Equal(t, "hash-1", u.PasswordHash, "existing admin password is not overwritten")

	users, err := s.ListUsers(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)

	profiles, err := s.ListAgentProfiles(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	p := profiles[0]
	require.Equal(t, DemoProfileName, p.Name)
	require.Equal(t, DemoGreeting, p.Greeting)
	require.Equal(t, "mn", p.Language)
	require.Contains(t, p.SystemPrompt, "эелдэг")
	require.Contains(t, p.Tools, "end_call")

	nums, err := s.ListSIPNumbers(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, nums, 1)
	require.Equal(t, DemoSIPNumber, nums[0].Number)
	require.NotNil(t, nums[0].AgentProfileID)
	require.Equal(t, p.ID, *nums[0].AgentProfileID)
	require.True(t, nums[0].AllowInbound && nums[0].AllowOutbound && nums[0].Active)

	// Concurrent seeding (several backend replicas starting) stays consistent.
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := s.SeedDemo(ctx, "admin@callgo.mn", "x")
			errs <- err
		}()
	}
	for range 4 {
		require.NoError(t, <-errs)
	}
	profiles, err = s.ListAgentProfiles(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, profiles, 1)

	_, err = s.SeedDemo(ctx, " ", "x")
	requireErrIs(t, err, domain.ErrInvalid)
}
