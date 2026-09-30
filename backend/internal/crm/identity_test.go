package crm

import (
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var saasTables = []string{
	"invitations", "password_resets", "email_verifications", "refresh_sessions", "api_keys", "audit_log",
	"subscriptions", "usage_records", "invoices", "payments",
}

func tableExists(t *testing.T, ctx context.Context, table string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&ok))
	return ok
}

// TestMigrationSaaSDownUp rolls 000005 (and anything newer) back on a schema
// holding SaaS data and re-applies it.
func TestMigrationSaaSDownUp(t *testing.T) {
	ctx, s := setup(t)
	t.Cleanup(func() {
		require.NoError(t, Migrate(context.Background(), testDSN))
		testPool.Reset()
	})
	org := newOrg(t, ctx, s, "saas-mig")
	u := &domain.User{OrgID: org.ID, Email: "mig@callgo.mn", Status: domain.UserInvited}
	require.NoError(t, s.CreateUser(ctx, u))
	require.NoError(t, s.AppendAudit(ctx, &domain.AuditEntry{OrgID: org.ID, Action: "user.invite"}))
	require.NoError(t, s.CreateInvoice(ctx, &domain.Invoice{OrgID: org.ID,
		PeriodStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}))

	var version int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&version))
	require.GreaterOrEqual(t, version, 5)

	require.NoError(t, runMigrations(ctx, testDSN, func(m *migrate.Migrate) error { return m.Migrate(4) }))
	testPool.Reset()
	for _, tbl := range saasTables {
		require.Falsef(t, tableExists(t, ctx, tbl), "%s after down", tbl)
	}
	for _, col := range []string{"plan_code", "status", "timezone", "settings", "updated_at"} {
		require.Falsef(t, columnExists(t, ctx, "organizations", col), "organizations.%s after down", col)
	}
	for _, col := range []string{"status", "email_verified_at", "last_login_at", "is_platform_admin", "updated_at"} {
		require.Falsef(t, columnExists(t, ctx, "users", col), "users.%s after down", col)
	}
	var seq bool
	require.NoError(t, testPool.QueryRow(ctx, `SELECT to_regclass('public.invoice_number_seq') IS NOT NULL`).Scan(&seq))
	require.False(t, seq)
	var n int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = 'mig@callgo.mn'`).Scan(&n))
	require.Equal(t, 1, n, "users survive the down migration")

	require.NoError(t, Migrate(ctx, testDSN))
	testPool.Reset()
	for _, tbl := range saasTables {
		require.Truef(t, tableExists(t, ctx, tbl), "%s after re-up", tbl)
	}
	got, err := s.GetUser(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, domain.UserActive, got.Status, "column default after re-up")
	o, err := s.GetOrg(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, "trial", o.PlanCode)
	require.Equal(t, "Asia/Ulaanbaatar", o.Timezone)
}

func TestOrgSaaSFields(t *testing.T) {
	ctx, s := setup(t)

	o := &domain.Organization{Name: "Acme", Slug: "acme"}
	require.NoError(t, s.CreateOrg(ctx, o))
	require.Equal(t, "trial", o.PlanCode)
	require.Equal(t, domain.OrgActive, o.Status)
	require.Equal(t, "Asia/Ulaanbaatar", o.Timezone)
	require.False(t, o.UpdatedAt.IsZero())

	got, err := s.GetOrg(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, "trial", got.PlanCode)
	require.Nil(t, got.Settings)

	requireErrIs(t, s.CreateOrg(ctx, &domain.Organization{Name: "x", Slug: "x", Timezone: "Mars/Olympus"}), domain.ErrInvalid)
	requireErrIs(t, s.CreateOrg(ctx, &domain.Organization{Name: "x", Slug: "x", Status: "bogus"}), domain.ErrInvalid)

	before := got.UpdatedAt
	upd := &domain.Organization{ID: o.ID, PlanCode: "starter", Status: domain.OrgSuspended, Timezone: "Asia/Hovd",
		Settings: map[string]any{"locale": "mn", "n": float64(3)}}
	require.NoError(t, s.UpdateOrg(ctx, upd))
	require.Equal(t, "Acme", upd.Name, "empty name keeps the stored value")
	require.Equal(t, "acme", upd.Slug)
	require.True(t, upd.UpdatedAt.After(before) || upd.UpdatedAt.Equal(before))

	got, err = s.GetOrg(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, "starter", got.PlanCode)
	require.Equal(t, domain.OrgSuspended, got.Status)
	require.Equal(t, "Asia/Hovd", got.Timezone)
	require.Equal(t, map[string]any{"locale": "mn", "n": float64(3)}, got.Settings)

	// nil settings keep; a non-nil map replaces.
	require.NoError(t, s.UpdateOrg(ctx, &domain.Organization{ID: o.ID, Name: "Acme LLC"}))
	got, err = s.GetOrg(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, "Acme LLC", got.Name)
	require.Equal(t, "mn", got.Settings["locale"])
	require.NoError(t, s.UpdateOrg(ctx, &domain.Organization{ID: o.ID, Settings: map[string]any{}}))
	got, err = s.GetOrg(ctx, o.ID)
	require.NoError(t, err)
	require.Nil(t, got.Settings)

	requireErrIs(t, s.UpdateOrg(ctx, &domain.Organization{ID: uuid.New(), Name: "n"}), domain.ErrNotFound)
	requireErrIs(t, s.UpdateOrg(ctx, &domain.Organization{ID: o.ID, Status: "bogus"}), domain.ErrInvalid)
	requireErrIs(t, s.UpdateOrg(ctx, &domain.Organization{ID: o.ID, Timezone: "Nope/Nope"}), domain.ErrInvalid)
}

func TestListOrgs(t *testing.T) {
	ctx, s := setup(t)
	for _, slug := range []string{"alpha", "beta", "gamma", "alpha-2", "under_score"} {
		newOrg(t, ctx, s, slug)
	}

	all, total, err := s.ListOrgs(ctx, "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, all, 5)

	page, total, err := s.ListOrgs(ctx, "", 2, 1)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, page, 2)
	require.Equal(t, all[1].ID, page[0].ID)

	hits, total, err := s.ListOrgs(ctx, "ALPHA", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, hits, 2)

	hits, total, err = s.ListOrgs(ctx, "_", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total, "LIKE metacharacters are literal")
	require.Equal(t, "under_score", hits[0].Slug)

	hits, total, err = s.ListOrgs(ctx, "zzz", 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.NotNil(t, hits)
	require.Empty(t, hits)
}

func TestUserSaaSFields(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "users")

	u := &domain.User{OrgID: org.ID, Email: "a@x.mn", Name: "A", PasswordHash: "h1"}
	require.NoError(t, s.CreateUser(ctx, u))
	require.Equal(t, domain.UserActive, u.Status)
	require.False(t, u.UpdatedAt.IsZero())

	inv := &domain.User{OrgID: org.ID, Email: "b@x.mn", Status: domain.UserInvited}
	require.NoError(t, s.CreateUser(ctx, inv))
	dis := &domain.User{OrgID: org.ID, Email: "c@x.mn", Status: domain.UserDisabled}
	require.NoError(t, s.CreateUser(ctx, dis))
	requireErrIs(t, s.CreateUser(ctx, &domain.User{OrgID: org.ID, Email: "d@x.mn", Status: "zombie"}), domain.ErrInvalid)

	n, err := s.CountUsers(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, 2, n, "invited + active count, disabled does not")
	n, err = s.CountUsers(ctx, uuid.New())
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = s.CountAllUsers(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	now := time.Now().UTC().Truncate(time.Microsecond)
	u.Name = "Alice"
	u.Role = domain.RoleAdmin
	u.Status = domain.UserDisabled
	u.PasswordHash = "h2"
	u.EmailVerifiedAt = &now
	u.LastLoginAt = &now
	u.IsPlatformAdmin = true
	require.NoError(t, s.UpdateUser(ctx, u))

	got, err := s.GetUser(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "Alice", got.Name)
	require.Equal(t, domain.RoleAdmin, got.Role)
	require.Equal(t, domain.UserDisabled, got.Status)
	require.Equal(t, "h2", got.PasswordHash)
	require.NotNil(t, got.EmailVerifiedAt)
	require.True(t, now.Equal(*got.EmailVerifiedAt))
	require.True(t, now.Equal(*got.LastLoginAt))
	require.True(t, got.IsPlatformAdmin)
	require.Equal(t, "a@x.mn", got.Email)

	// Empty role/status/password keep the stored values; nil timestamps clear.
	require.NoError(t, s.UpdateUser(ctx, &domain.User{ID: u.ID, Name: "Al"}))
	got, err = s.GetUserByEmail(ctx, "A@X.MN")
	require.NoError(t, err)
	require.Equal(t, "Al", got.Name)
	require.Equal(t, domain.RoleAdmin, got.Role)
	require.Equal(t, domain.UserDisabled, got.Status)
	require.Equal(t, "h2", got.PasswordHash)
	require.Nil(t, got.EmailVerifiedAt)
	require.Nil(t, got.LastLoginAt)
	require.False(t, got.IsPlatformAdmin)

	requireErrIs(t, s.UpdateUser(ctx, &domain.User{ID: uuid.New()}), domain.ErrNotFound)
	requireErrIs(t, s.UpdateUser(ctx, &domain.User{ID: u.ID, Role: "king"}), domain.ErrInvalid)

	users, err := s.ListUsers(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, users, 3)
	require.Equal(t, domain.UserInvited, users[1].Status)
}

func TestInvitations(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "inv")
	other := newOrg(t, ctx, s, "inv-other")
	admin := &domain.User{OrgID: org.ID, Email: "boss@x.mn", Role: domain.RoleOwner}
	require.NoError(t, s.CreateUser(ctx, admin))
	exp := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Microsecond)

	inv := &domain.Invitation{OrgID: org.ID, Email: " New@X.mn ", TokenHash: "h-inv-1", InvitedBy: &admin.ID, ExpiresAt: exp}
	require.NoError(t, s.CreateInvitation(ctx, inv))
	require.NotEqual(t, uuid.Nil, inv.ID)
	require.Equal(t, "New@X.mn", inv.Email)
	require.Equal(t, domain.RoleOperator, inv.Role)
	require.False(t, inv.CreatedAt.IsZero())

	// Same address (any case) in the same org while pending → conflict.
	requireErrIs(t, s.CreateInvitation(ctx, &domain.Invitation{OrgID: org.ID, Email: "new@x.mn", TokenHash: "h-inv-2",
		ExpiresAt: exp}), domain.ErrConflict)
	// Duplicate token hash → conflict.
	requireErrIs(t, s.CreateInvitation(ctx, &domain.Invitation{OrgID: other.ID, Email: "z@x.mn", TokenHash: "h-inv-1",
		ExpiresAt: exp}), domain.ErrConflict)
	// Another org may invite the same address.
	require.NoError(t, s.CreateInvitation(ctx, &domain.Invitation{OrgID: other.ID, Email: "new@x.mn", TokenHash: "h-inv-3",
		Role: domain.RoleAdmin, ExpiresAt: exp}))
	requireErrIs(t, s.CreateInvitation(ctx, &domain.Invitation{OrgID: org.ID, Email: "r@x.mn", TokenHash: "h-r",
		Role: "king", ExpiresAt: exp}), domain.ErrInvalid)

	got, err := s.GetInvitationByHash(ctx, "h-inv-1")
	require.NoError(t, err)
	require.Equal(t, inv.ID, got.ID)
	require.Equal(t, admin.ID, *got.InvitedBy)
	require.True(t, exp.Equal(got.ExpiresAt))
	require.Nil(t, got.AcceptedAt)
	require.Equal(t, "h-inv-1", got.TokenHash)
	_, err = s.GetInvitationByHash(ctx, "nope")
	requireErrIs(t, err, domain.ErrNotFound)

	second := &domain.Invitation{OrgID: org.ID, Email: "two@x.mn", TokenHash: "h-inv-4", ExpiresAt: exp}
	require.NoError(t, s.CreateInvitation(ctx, second))
	list, err := s.ListInvitations(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)

	require.NoError(t, s.MarkInvitationAccepted(ctx, inv.ID))
	requireErrIs(t, s.MarkInvitationAccepted(ctx, inv.ID), domain.ErrConflict)
	requireErrIs(t, s.MarkInvitationAccepted(ctx, uuid.New()), domain.ErrNotFound)
	got, err = s.GetInvitationByHash(ctx, "h-inv-1")
	require.NoError(t, err)
	require.NotNil(t, got.AcceptedAt)

	list, err = s.ListInvitations(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "accepted invitations are not listed")
	require.Equal(t, second.ID, list[0].ID)

	// Once accepted, the address may be invited again.
	require.NoError(t, s.CreateInvitation(ctx, &domain.Invitation{OrgID: org.ID, Email: "new@x.mn", TokenHash: "h-inv-5",
		ExpiresAt: exp}))

	require.NoError(t, s.DeleteInvitation(ctx, second.ID))
	requireErrIs(t, s.DeleteInvitation(ctx, second.ID), domain.ErrNotFound)
	_, err = s.GetInvitationByHash(ctx, "h-inv-4")
	requireErrIs(t, err, domain.ErrNotFound)

	// Deleting the inviter keeps the invitation.
	_, err = testPool.Exec(ctx, `DELETE FROM users WHERE id = $1`, admin.ID)
	require.NoError(t, err)
	got, err = s.GetInvitationByHash(ctx, "h-inv-1")
	require.NoError(t, err)
	require.Nil(t, got.InvitedBy)
}

func TestPasswordResetsAndEmailVerifications(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "tok")
	u := &domain.User{OrgID: org.ID, Email: "u@x.mn"}
	require.NoError(t, s.CreateUser(ctx, u))
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)

	r := &domain.PasswordReset{UserID: u.ID, TokenHash: "pr-1", ExpiresAt: exp}
	require.NoError(t, s.CreatePasswordReset(ctx, r))
	require.NotEqual(t, uuid.Nil, r.ID)
	require.False(t, r.CreatedAt.IsZero())
	requireErrIs(t, s.CreatePasswordReset(ctx, &domain.PasswordReset{UserID: u.ID, TokenHash: "pr-1", ExpiresAt: exp}),
		domain.ErrConflict)
	requireErrIs(t, s.CreatePasswordReset(ctx, &domain.PasswordReset{UserID: uuid.New(), TokenHash: "pr-2", ExpiresAt: exp}),
		domain.ErrInvalid)

	gotR, err := s.GetPasswordResetByHash(ctx, "pr-1")
	require.NoError(t, err)
	require.Equal(t, r.ID, gotR.ID)
	require.Equal(t, u.ID, gotR.UserID)
	require.True(t, exp.Equal(gotR.ExpiresAt))
	require.Nil(t, gotR.UsedAt)
	_, err = s.GetPasswordResetByHash(ctx, "missing")
	requireErrIs(t, err, domain.ErrNotFound)

	require.NoError(t, s.MarkPasswordResetUsed(ctx, r.ID))
	requireErrIs(t, s.MarkPasswordResetUsed(ctx, r.ID), domain.ErrConflict)
	requireErrIs(t, s.MarkPasswordResetUsed(ctx, uuid.New()), domain.ErrNotFound)
	gotR, err = s.GetPasswordResetByHash(ctx, "pr-1")
	require.NoError(t, err)
	require.NotNil(t, gotR.UsedAt)

	v := &domain.EmailVerification{UserID: u.ID, TokenHash: "ev-1", ExpiresAt: exp}
	require.NoError(t, s.CreateEmailVerification(ctx, v))
	require.NotEqual(t, uuid.Nil, v.ID)
	requireErrIs(t, s.CreateEmailVerification(ctx, &domain.EmailVerification{UserID: u.ID, TokenHash: "ev-1", ExpiresAt: exp}),
		domain.ErrConflict)
	gotV, err := s.GetEmailVerificationByHash(ctx, "ev-1")
	require.NoError(t, err)
	require.Equal(t, v.ID, gotV.ID)
	require.Nil(t, gotV.UsedAt)
	_, err = s.GetEmailVerificationByHash(ctx, "missing")
	requireErrIs(t, err, domain.ErrNotFound)
	require.NoError(t, s.MarkEmailVerificationUsed(ctx, v.ID))
	requireErrIs(t, s.MarkEmailVerificationUsed(ctx, v.ID), domain.ErrConflict)
	requireErrIs(t, s.MarkEmailVerificationUsed(ctx, uuid.New()), domain.ErrNotFound)
	gotV, err = s.GetEmailVerificationByHash(ctx, "ev-1")
	require.NoError(t, err)
	require.NotNil(t, gotV.UsedAt)

	// Tokens go with their user.
	_, err = testPool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.ID)
	require.NoError(t, err)
	_, err = s.GetPasswordResetByHash(ctx, "pr-1")
	requireErrIs(t, err, domain.ErrNotFound)
	_, err = s.GetEmailVerificationByHash(ctx, "ev-1")
	requireErrIs(t, err, domain.ErrNotFound)
}

func TestRefreshSessions(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "rs")
	u := &domain.User{OrgID: org.ID, Email: "u@rs.mn"}
	require.NoError(t, s.CreateUser(ctx, u))
	u2 := &domain.User{OrgID: org.ID, Email: "u2@rs.mn"}
	require.NoError(t, s.CreateUser(ctx, u2))
	exp := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Microsecond)

	rs := &domain.RefreshSession{UserID: u.ID, OrgID: org.ID, TokenHash: "rt-1", UserAgent: "curl", IP: "10.0.0.1",
		ExpiresAt: exp}
	require.NoError(t, s.CreateRefreshSession(ctx, rs))
	require.NotEqual(t, uuid.Nil, rs.ID)
	require.False(t, rs.LastUsed.IsZero())
	require.False(t, rs.CreatedAt.IsZero())
	requireErrIs(t, s.CreateRefreshSession(ctx, &domain.RefreshSession{UserID: u.ID, OrgID: org.ID, TokenHash: "rt-1",
		ExpiresAt: exp}), domain.ErrConflict)

	got, err := s.GetRefreshSessionByHash(ctx, "rt-1")
	require.NoError(t, err)
	require.Equal(t, rs.ID, got.ID)
	require.Equal(t, "curl", got.UserAgent)
	require.Equal(t, "10.0.0.1", got.IP)
	require.True(t, exp.Equal(got.ExpiresAt))
	require.Nil(t, got.RevokedAt)

	// Rotation swaps the hash atomically: the old token stops resolving.
	exp2 := exp.Add(time.Hour)
	require.NoError(t, s.RotateRefreshSession(ctx, rs.ID, "rt-2", exp2))
	_, err = s.GetRefreshSessionByHash(ctx, "rt-1")
	requireErrIs(t, err, domain.ErrNotFound)
	got, err = s.GetRefreshSessionByHash(ctx, "rt-2")
	require.NoError(t, err)
	require.Equal(t, rs.ID, got.ID)
	require.True(t, exp2.Equal(got.ExpiresAt))
	require.False(t, got.LastUsed.Before(rs.LastUsed))
	requireErrIs(t, s.RotateRefreshSession(ctx, uuid.New(), "rt-x", exp2), domain.ErrNotFound)

	// A second session; rotating onto its hash collides.
	rs2 := &domain.RefreshSession{UserID: u.ID, OrgID: org.ID, TokenHash: "rt-3", ExpiresAt: exp}
	require.NoError(t, s.CreateRefreshSession(ctx, rs2))
	requireErrIs(t, s.RotateRefreshSession(ctx, rs.ID, "rt-3", exp2), domain.ErrConflict)

	// Expired sessions cannot rotate.
	expired := &domain.RefreshSession{UserID: u.ID, OrgID: org.ID, TokenHash: "rt-old",
		ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, s.CreateRefreshSession(ctx, expired))
	requireErrIs(t, s.RotateRefreshSession(ctx, expired.ID, "rt-new", exp), domain.ErrConflict)

	list, err := s.ListRefreshSessions(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, list, 2, "expired sessions are not listed")

	// Revoked sessions cannot rotate; revoke is idempotent.
	require.NoError(t, s.RevokeRefreshSession(ctx, rs.ID))
	got, err = s.GetRefreshSessionByHash(ctx, "rt-2")
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
	first := *got.RevokedAt
	require.NoError(t, s.RevokeRefreshSession(ctx, rs.ID))
	got, err = s.GetRefreshSessionByHash(ctx, "rt-2")
	require.NoError(t, err)
	require.True(t, first.Equal(*got.RevokedAt), "first revocation time kept")
	requireErrIs(t, s.RotateRefreshSession(ctx, rs.ID, "rt-9", exp2), domain.ErrConflict)
	requireErrIs(t, s.RevokeRefreshSession(ctx, uuid.New()), domain.ErrNotFound)

	list, err = s.ListRefreshSessions(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, rs2.ID, list[0].ID)

	other := &domain.RefreshSession{UserID: u2.ID, OrgID: org.ID, TokenHash: "rt-u2", ExpiresAt: exp}
	require.NoError(t, s.CreateRefreshSession(ctx, other))
	require.NoError(t, s.RevokeUserSessions(ctx, u.ID))
	require.NoError(t, s.RevokeUserSessions(ctx, u.ID), "no live sessions left is fine")
	list, err = s.ListRefreshSessions(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, list)
	require.Empty(t, list)
	list, err = s.ListRefreshSessions(ctx, u2.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "other users are untouched")
}

func TestAPIKeys(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "keys")
	other := newOrg(t, ctx, s, "keys-other")
	u := &domain.User{OrgID: org.ID, Email: "k@x.mn"}
	require.NoError(t, s.CreateUser(ctx, u))

	k := &domain.APIKey{OrgID: org.ID, Name: "CRM sync", Prefix: "abcd1234", KeyHash: "kh-1",
		Scopes: []string{"calls:read", "contacts:write"}, CreatedBy: &u.ID}
	require.NoError(t, s.CreateAPIKey(ctx, k))
	require.NotEqual(t, uuid.Nil, k.ID)
	require.False(t, k.CreatedAt.IsZero())
	requireErrIs(t, s.CreateAPIKey(ctx, &domain.APIKey{OrgID: other.ID, Prefix: "abcd1234", KeyHash: "kh-2"}),
		domain.ErrConflict)
	requireErrIs(t, s.CreateAPIKey(ctx, &domain.APIKey{OrgID: org.ID, Prefix: "", KeyHash: "kh-2"}), domain.ErrInvalid)

	k2 := &domain.APIKey{OrgID: org.ID, Name: "no scopes", Prefix: "zzzz9999", KeyHash: "kh-3"}
	require.NoError(t, s.CreateAPIKey(ctx, k2))
	require.NotNil(t, k2.Scopes)

	got, err := s.GetAPIKeyByPrefix(ctx, "abcd1234")
	require.NoError(t, err)
	require.Equal(t, k.ID, got.ID)
	require.Equal(t, "kh-1", got.KeyHash)
	require.Equal(t, []string{"calls:read", "contacts:write"}, got.Scopes)
	require.Equal(t, u.ID, *got.CreatedBy)
	require.Nil(t, got.LastUsedAt)
	require.Nil(t, got.RevokedAt)
	_, err = s.GetAPIKeyByPrefix(ctx, "nope")
	requireErrIs(t, err, domain.ErrNotFound)

	require.NoError(t, s.TouchAPIKey(ctx, k.ID))
	requireErrIs(t, s.TouchAPIKey(ctx, uuid.New()), domain.ErrNotFound)
	got, err = s.GetAPIKeyByPrefix(ctx, "abcd1234")
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)

	require.NoError(t, s.RevokeAPIKey(ctx, k.ID))
	require.NoError(t, s.RevokeAPIKey(ctx, k.ID))
	requireErrIs(t, s.RevokeAPIKey(ctx, uuid.New()), domain.ErrNotFound)
	got, err = s.GetAPIKeyByPrefix(ctx, "abcd1234")
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt, "revoked keys still resolve so the caller can reject them")

	keys, err := s.ListAPIKeys(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Equal(t, k2.ID, keys[0].ID, "newest first")
	require.Equal(t, []string{}, keys[0].Scopes)
	keys, err = s.ListAPIKeys(ctx, other.ID)
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestAudit(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "audit")
	other := newOrg(t, ctx, s, "audit-other")
	alice := &domain.User{OrgID: org.ID, Email: "alice@x.mn"}
	require.NoError(t, s.CreateUser(ctx, alice))
	bob := &domain.User{OrgID: org.ID, Email: "bob@x.mn"}
	require.NoError(t, s.CreateUser(ctx, bob))

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	entries := []domain.AuditEntry{
		{OrgID: org.ID, ActorID: &alice.ID, ActorEmail: alice.Email, Action: "user.invite", TargetType: "user",
			TargetID: "bob@x.mn", Meta: map[string]any{"role": "operator"}, IP: "1.2.3.4", At: base},
		{OrgID: org.ID, ActorID: &alice.ID, ActorEmail: alice.Email, Action: "user.disable", At: base.Add(time.Hour)},
		{OrgID: org.ID, ActorID: &bob.ID, ActorEmail: bob.Email, Action: "campaign.start", At: base.Add(2 * time.Hour)},
		{OrgID: org.ID, ActorEmail: "system", Action: "llm_config.update", At: base.Add(3 * time.Hour)},
		{OrgID: other.ID, ActorEmail: "x", Action: "user.invite", At: base},
	}
	for i := range entries {
		require.NoError(t, s.AppendAudit(ctx, &entries[i]))
		require.NotEqual(t, uuid.Nil, entries[i].ID)
	}
	now := &domain.AuditEntry{OrgID: org.ID, Action: "org.update"}
	require.NoError(t, s.AppendAudit(ctx, now))
	require.WithinDuration(t, time.Now(), now.At, time.Minute, "At defaults to now")
	requireErrIs(t, s.AppendAudit(ctx, &domain.AuditEntry{OrgID: org.ID}), domain.ErrInvalid)

	all, total, err := s.ListAudit(ctx, org.ID, domain.AuditFilter{})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, all, 5)
	require.Equal(t, "org.update", all[0].Action, "newest first")
	first := all[len(all)-1]
	require.Equal(t, "user.invite", first.Action)
	require.Equal(t, map[string]any{"role": "operator"}, first.Meta)
	require.Equal(t, "1.2.3.4", first.IP)
	require.Equal(t, "bob@x.mn", first.TargetID)
	require.Equal(t, alice.ID, *first.ActorID)
	require.True(t, base.Equal(first.At))
	require.Nil(t, all[1].Meta)

	got, total, err := s.ListAudit(ctx, org.ID, domain.AuditFilter{ActorID: &alice.ID})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, got, 2)

	got, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{Action: "campaign.start"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, bob.ID, *got[0].ActorID)

	_, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{Action: "user."})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	_, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{Action: "user.*"})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	_, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{Action: "user"})
	require.NoError(t, err)
	require.Zero(t, total, "without a trailing . or * the action matches exactly")

	from, to := base.Add(time.Hour), base.Add(3*time.Hour)
	got, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{From: &from, To: &to})
	require.NoError(t, err)
	require.Equal(t, 2, total, "From inclusive, To exclusive")
	require.Equal(t, "campaign.start", got[0].Action)
	require.Equal(t, "user.disable", got[1].Action)

	got, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{ActorID: &alice.ID, From: &from})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, "user.disable", got[0].Action)

	got, total, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, got, 2)
	require.Equal(t, all[2].ID, got[0].ID)

	// Deleting the actor keeps the entry.
	_, err = testPool.Exec(ctx, `DELETE FROM users WHERE id = $1`, bob.ID)
	require.NoError(t, err)
	got, _, err = s.ListAudit(ctx, org.ID, domain.AuditFilter{Action: "campaign.start"})
	require.NoError(t, err)
	require.Nil(t, got[0].ActorID)
	require.Equal(t, "bob@x.mn", got[0].ActorEmail)
}

func TestSeedDemoSaaS(t *testing.T) {
	ctx, s := setup(t)
	org, err := s.SeedDemo(ctx, "admin@callgo.mn", "hash")
	require.NoError(t, err)
	require.Equal(t, "trial", org.PlanCode)
	require.Equal(t, domain.OrgActive, org.Status)

	admin, err := s.GetUserByEmail(ctx, "admin@callgo.mn")
	require.NoError(t, err)
	require.Equal(t, domain.UserActive, admin.Status)
	require.NotNil(t, admin.EmailVerifiedAt)
	require.True(t, admin.IsPlatformAdmin)

	sub, err := s.GetSubscription(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, "trial", sub.PlanCode)
	require.Equal(t, domain.SubTrialing, sub.Status)
	require.WithinDuration(t, time.Now(), sub.CurrentPeriodStart, time.Minute)
	require.WithinDuration(t, sub.CurrentPeriodStart.Add(14*24*time.Hour), sub.CurrentPeriodEnd, time.Second)
	require.NotNil(t, sub.TrialEndsAt)
	require.True(t, sub.CurrentPeriodEnd.Equal(*sub.TrialEndsAt))

	// Re-seeding keeps the existing subscription.
	_, err = s.SeedDemo(ctx, "admin@callgo.mn", "hash-2")
	require.NoError(t, err)
	again, err := s.GetSubscription(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, sub.ID, again.ID)
	require.True(t, sub.CurrentPeriodEnd.Equal(again.CurrentPeriodEnd))
}
