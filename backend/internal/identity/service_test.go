package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var ctx = context.Background()

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Acme LLC":                  "acme-llc",
		"  --Hello,  World!!-- ":    "hello-world",
		"Номин Трейд ХХК":           "nomin-treid-khkhk",
		"Өлзий & Үүрийн Цолмон":     "olzii-uuriin-tsolmon",
		"🚀":                         "",
		strings.Repeat("abc ", 30):  "abc-abc-abc-abc-abc-abc-abc-abc-abc-abc",
		"Mongolia 2026 — Хөгжил №1": "mongolia-2026-khogjil-1",
	} {
		assert.Equal(t, want, Slugify(in), in)
	}
}

func TestSignup(t *testing.T) {
	f := newFixture(t)
	res := f.signup(t, "Acme LLC", "Owner@Example.MN")

	assert.Equal(t, "acme-llc", res.Org.Slug)
	assert.Equal(t, domain.OrgActive, res.Org.Status)
	assert.Equal(t, "trial", res.Org.PlanCode)
	assert.Equal(t, DefaultTimezone, res.Org.Timezone)
	assert.Equal(t, "owner@example.mn", res.User.Email)
	assert.Equal(t, domain.RoleOwner, res.User.Role)
	assert.Equal(t, domain.UserActive, res.User.Status)
	assert.Nil(t, res.User.EmailVerifiedAt)
	require.NotNil(t, res.Subscription)
	assert.Equal(t, domain.SubTrialing, res.Subscription.Status)

	c, err := auth.ParseToken(testSecret, res.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, res.User.ID, c.UserID)
	assert.Equal(t, res.Org.ID, c.OrgID)
	assert.Equal(t, domain.RoleOwner, c.Role)

	// Refresh token stored hashed only.
	sess, err := f.repo.GetRefreshSessionByHash(ctx, HashToken(res.RefreshToken))
	require.NoError(t, err)
	assert.Equal(t, client.IP, sess.IP)
	assert.Equal(t, client.UserAgent, sess.UserAgent)
	assert.Equal(t, f.clock.Now().Add(30*24*time.Hour), sess.ExpiresAt)
	assert.NotContains(t, sess.TokenHash, res.RefreshToken)

	// Verification email with link.
	require.Equal(t, 1, f.mail.count())
	tok := f.mail.lastToken(t, "owner@example.mn", "/verify-email")
	assert.NotEmpty(t, tok)
	assert.Contains(t, f.mail.sent[0].text, "https://app.callgo.mn/verify-email?token=")
	assert.Contains(t, f.repo.Actions(), "org.signup")

	// Slug dedupe and Cyrillic names.
	assert.Equal(t, "acme-llc-2", f.signup(t, "ACME llc", "b@example.mn").Org.Slug)
	assert.Equal(t, "acme-llc-3", f.signup(t, "Acme, LLC", "c@example.mn").Org.Slug)
	assert.Equal(t, "nomin", f.signup(t, "Номин", "d@example.mn").Org.Slug)
	assert.Equal(t, "org", f.signup(t, "!!!", "e@example.mn").Org.Slug)

	// Validation.
	for _, in := range []SignupInput{
		{OrgName: "", Email: "x@example.mn", Password: "password1", Name: "X"},
		{OrgName: "X", Email: "x@example.mn", Password: "password1", Name: " "},
		{OrgName: "X", Email: "not-an-email", Password: "password1", Name: "X"},
		{OrgName: "X", Email: "x@localhost", Password: "password1", Name: "X"},
		{OrgName: "X", Email: "x@example.mn", Password: "short", Name: "X"},
		{OrgName: "X", Email: "x@example.mn", Password: strings.Repeat("я", 40), Name: "X"},
	} {
		_, err := f.svc.Signup(ctx, in, client)
		assert.ErrorIs(t, err, domain.ErrInvalid, "%+v", in)
	}
	_, err = f.svc.Signup(ctx, SignupInput{OrgName: "Dup", Email: "OWNER@example.mn", Password: "password1", Name: "D"}, client)
	assert.ErrorIs(t, err, domain.ErrConflict)
	msg, ok := PublicMessage(err)
	assert.True(t, ok)
	assert.Equal(t, "email already registered", msg)

	f.svc.cfg.AllowSignup = false
	_, err = f.svc.Signup(ctx, SignupInput{OrgName: "Z", Email: "z@example.mn", Password: "password1", Name: "Z"}, client)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

func TestSignupSurvivesTrialFailure(t *testing.T) {
	f := newFixture(t)
	f.subs.failOn = true
	res := f.signup(t, "Acme", "a@example.mn")
	assert.Nil(t, res.Subscription)
	assert.NotEmpty(t, res.AccessToken)
}

func TestLogin(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "owner@example.mn")
	f.clock.Advance(time.Hour)

	res, err := f.svc.Login(ctx, " OWNER@example.mn ", "password1", client)
	require.NoError(t, err)
	assert.NotEmpty(t, res.AccessToken)
	assert.NotEmpty(t, res.RefreshToken)
	assert.NotEqual(t, s.RefreshToken, res.RefreshToken)
	require.NotNil(t, res.Subscription)
	u, _ := f.repo.GetUser(ctx, s.User.ID)
	require.NotNil(t, u.LastLoginAt)
	assert.Equal(t, f.clock.Now(), *u.LastLoginAt)

	_, err = f.svc.Login(ctx, "owner@example.mn", "wrong-pass", client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.Login(ctx, "nobody@example.mn", "password1", client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.Login(ctx, "", "", client)
	assert.ErrorIs(t, err, domain.ErrInvalid)

	// Suspended orgs may log in (to pay); closed orgs may not.
	org, _ := f.repo.GetOrg(ctx, s.Org.ID)
	org.Status = domain.OrgSuspended
	require.NoError(t, f.repo.UpdateOrg(ctx, org))
	_, err = f.svc.Login(ctx, "owner@example.mn", "password1", client)
	require.NoError(t, err)
	org.Status = domain.OrgClosed
	require.NoError(t, f.repo.UpdateOrg(ctx, org))
	_, err = f.svc.Login(ctx, "owner@example.mn", "password1", client)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	org.Status = domain.OrgActive
	require.NoError(t, f.repo.UpdateOrg(ctx, org))

	for _, st := range []domain.UserStatus{domain.UserDisabled, domain.UserInvited} {
		u.Status = st
		require.NoError(t, f.repo.UpdateUser(ctx, u))
		_, err = f.svc.Login(ctx, "owner@example.mn", "password1", client)
		assert.ErrorIs(t, err, domain.ErrForbidden, st)
	}
	// Legacy users without a status can log in.
	u.Status = ""
	require.NoError(t, f.repo.UpdateUser(ctx, u))
	_, err = f.svc.Login(ctx, "owner@example.mn", "password1", client)
	require.NoError(t, err)
}

func TestLoginPlatformAdminClaim(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "staff@callgo.mn")
	u, _ := f.repo.GetUser(ctx, s.User.ID)
	u.IsPlatformAdmin = true
	require.NoError(t, f.repo.UpdateUser(ctx, u))
	res, err := f.svc.Login(ctx, "staff@callgo.mn", "password1", client)
	require.NoError(t, err)
	c, err := auth.ParseToken(testSecret, res.AccessToken)
	require.NoError(t, err)
	assert.True(t, c.PlatformAdmin)
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "owner@example.mn")
	other, err := f.svc.Login(ctx, "owner@example.mn", "password1", client) // second device
	require.NoError(t, err)

	f.clock.Advance(time.Minute)
	r1, err := f.svc.Refresh(ctx, s.RefreshToken, client)
	require.NoError(t, err)
	assert.NotEqual(t, s.RefreshToken, r1.RefreshToken)
	c, err := auth.ParseToken(testSecret, r1.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, s.User.ID, c.UserID)

	r2, err := f.svc.Refresh(ctx, r1.RefreshToken, client)
	require.NoError(t, err)
	sessions, _ := f.svc.ListSessions(ctx, s.User.ID)
	assert.Len(t, sessions, 2, "rotation keeps one session per device")

	// Reusing a rotated token revokes every session of the user.
	_, err = f.svc.Refresh(ctx, s.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	sessions, _ = f.svc.ListSessions(ctx, s.User.ID)
	assert.Empty(t, sessions)
	_, err = f.svc.Refresh(ctx, r2.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.Refresh(ctx, other.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	assert.Contains(t, f.repo.Actions(), "session.reuse_detected")
}

func TestRefreshRejectsForgedExpiredAndLoggedOut(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "owner@example.mn")

	// Forged / garbage tokens do not trigger reuse revocation.
	for _, bad := range []string{"", "garbage", s.RefreshToken[:len(s.RefreshToken)-2] + "AA"} {
		_, err := f.svc.Refresh(ctx, bad, client)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
	}
	forged, err := (&Service{refreshKey: []byte("other key")}).newRefreshToken(s.User.ID, uuid.New())
	require.NoError(t, err)
	_, err = f.svc.Refresh(ctx, forged, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	sessions, _ := f.svc.ListSessions(ctx, s.User.ID)
	assert.Len(t, sessions, 1)

	// Logout revokes; unknown tokens are ignored.
	require.NoError(t, f.svc.Logout(ctx, s.RefreshToken))
	require.NoError(t, f.svc.Logout(ctx, s.RefreshToken))
	require.NoError(t, f.svc.Logout(ctx, "unknown"))
	_, err = f.svc.Refresh(ctx, s.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)

	// Expiry.
	res, err := f.svc.Login(ctx, "owner@example.mn", "password1", client)
	require.NoError(t, err)
	f.clock.Advance(31 * 24 * time.Hour)
	_, err = f.svc.Refresh(ctx, res.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)

	// Disabled users cannot refresh.
	f.clock.Advance(-31 * 24 * time.Hour)
	res, err = f.svc.Login(ctx, "owner@example.mn", "password1", client)
	require.NoError(t, err)
	u, _ := f.repo.GetUser(ctx, s.User.ID)
	u.Status = domain.UserDisabled
	require.NoError(t, f.repo.UpdateUser(ctx, u))
	_, err = f.svc.Refresh(ctx, res.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestVerifyEmailAndResend(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "owner@example.mn")
	tok := f.mail.lastToken(t, "owner@example.mn", "/verify-email")

	_, err := f.svc.VerifyEmail(ctx, "nope")
	assert.ErrorIs(t, err, domain.ErrInvalid)

	u, err := f.svc.VerifyEmail(ctx, tok)
	require.NoError(t, err)
	require.NotNil(t, u.EmailVerifiedAt)
	stored, _ := f.repo.GetUser(ctx, s.User.ID)
	assert.NotNil(t, stored.EmailVerifiedAt)
	_, err = f.svc.VerifyEmail(ctx, tok)
	assert.ErrorIs(t, err, domain.ErrInvalid, "single use")

	// Resend: no mail for verified or unknown users.
	n := f.mail.count()
	require.NoError(t, f.svc.ResendVerification(ctx, "owner@example.mn"))
	require.NoError(t, f.svc.ResendVerification(ctx, "ghost@example.mn"))
	assert.Equal(t, n, f.mail.count())

	s2 := f.signup(t, "Beta", "beta@example.mn")
	require.NoError(t, f.svc.ResendVerification(ctx, "BETA@example.mn"))
	tok2 := f.mail.lastToken(t, "beta@example.mn", "/verify-email")
	f.clock.Advance(25 * time.Hour)
	_, err = f.svc.VerifyEmail(ctx, tok2)
	assert.ErrorIs(t, err, domain.ErrInvalid, "expired")
	stored, _ = f.repo.GetUser(ctx, s2.User.ID)
	assert.Nil(t, stored.EmailVerifiedAt)
}

func TestForgotAndResetPassword(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "owner@example.mn")
	n := f.mail.count()
	f.svc.ForgotPassword(ctx, "ghost@example.mn")
	f.svc.ForgotPassword(ctx, "garbage")
	assert.Equal(t, n, f.mail.count(), "no mail for unknown addresses")

	f.svc.ForgotPassword(ctx, "Owner@Example.mn")
	require.Equal(t, n+1, f.mail.count())
	tok := f.mail.lastToken(t, "owner@example.mn", "/reset-password")
	for _, r := range f.repo.Resets {
		assert.Equal(t, HashToken(tok), r.TokenHash, "stored as sha256")
		assert.Equal(t, f.clock.Now().Add(time.Hour), r.ExpiresAt)
	}

	_, err := f.svc.ResetPassword(ctx, tok, "short")
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.ResetPassword(ctx, "wrong", "newpassword1")
	assert.ErrorIs(t, err, domain.ErrInvalid)

	u, err := f.svc.ResetPassword(ctx, tok, "newpassword1")
	require.NoError(t, err)
	assert.Equal(t, s.User.ID, u.ID)
	assert.NotNil(t, u.EmailVerifiedAt, "reset proves the mailbox")
	_, err = f.svc.Refresh(ctx, s.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "sessions revoked")
	_, err = f.svc.Login(ctx, "owner@example.mn", "password1", client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.Login(ctx, "owner@example.mn", "newpassword1", client)
	require.NoError(t, err)
	_, err = f.svc.ResetPassword(ctx, tok, "another-pass")
	assert.ErrorIs(t, err, domain.ErrInvalid, "single use")

	f.svc.ForgotPassword(ctx, "owner@example.mn")
	tok = f.mail.lastToken(t, "owner@example.mn", "/reset-password")
	f.clock.Advance(61 * time.Minute)
	_, err = f.svc.ResetPassword(ctx, tok, "another-pass")
	assert.ErrorIs(t, err, domain.ErrInvalid, "expired")
}

func TestChangePasswordAndSessions(t *testing.T) {
	f := newFixture(t)
	s := f.signup(t, "Acme", "owner@example.mn")
	other, err := f.svc.Login(ctx, "owner@example.mn", "password1", Client{IP: "198.51.100.1", UserAgent: "phone"})
	require.NoError(t, err)

	err = f.svc.ChangePassword(ctx, s.User.ID, "bad-current", "newpassword1", "")
	assert.ErrorIs(t, err, domain.ErrInvalid)
	err = f.svc.ChangePassword(ctx, s.User.ID, "password1", "short", "")
	assert.ErrorIs(t, err, domain.ErrInvalid)
	err = f.svc.ChangePassword(ctx, s.User.ID, "password1", "password1", "")
	assert.ErrorIs(t, err, domain.ErrInvalid)

	require.NoError(t, f.svc.ChangePassword(ctx, s.User.ID, "password1", "newpassword1", s.RefreshToken))
	_, err = f.svc.Refresh(ctx, other.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "other sessions revoked")
	kept, err := f.svc.Refresh(ctx, s.RefreshToken, client)
	require.NoError(t, err, "caller's session kept")
	_, err = f.svc.Login(ctx, "owner@example.mn", "newpassword1", client)
	require.NoError(t, err)

	sessions, err := f.svc.ListSessions(ctx, s.User.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.NoError(t, f.svc.RevokeSession(ctx, s.User.ID, sessions[0].ID))
	require.NoError(t, f.svc.RevokeSession(ctx, s.User.ID, sessions[0].ID), "idempotent")
	sessions2, _ := f.svc.ListSessions(ctx, s.User.ID)
	assert.Len(t, sessions2, 1)

	// Cannot revoke another user's session.
	b := f.signup(t, "Beta", "beta@example.mn")
	err = f.svc.RevokeSession(ctx, b.User.ID, sessions2[0].ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_ = kept
}

func TestInviteAcceptAndQuota(t *testing.T) {
	f := newFixture(t)
	owner := f.signup(t, "Acme", "owner@example.mn")
	f.plan.MaxUsers = 3

	inv, err := f.svc.Invite(ctx, actorOf(owner), "Op@Example.mn", domain.RoleOperator)
	require.NoError(t, err)
	assert.Equal(t, "op@example.mn", inv.Email)
	assert.Equal(t, f.clock.Now().Add(7*24*time.Hour), inv.ExpiresAt)
	require.NotNil(t, inv.InvitedBy)
	tok := f.mail.lastToken(t, "op@example.mn", "/accept-invitation")
	assert.Equal(t, HashToken(tok), inv.TokenHash)
	assert.Contains(t, f.mail.sent[len(f.mail.sent)-1].text, "Acme")

	// Re-inviting the same email replaces the pending invitation (no extra seat).
	inv2, err := f.svc.Invite(ctx, actorOf(owner), "op@example.mn", domain.RoleAdmin)
	require.NoError(t, err)
	users, invs, err := f.svc.ListMembers(ctx, owner.Org.ID)
	require.NoError(t, err)
	assert.Len(t, users, 1)
	require.Len(t, invs, 1)
	assert.Equal(t, inv2.ID, invs[0].ID)

	// owner + op pending + second pending = 3 = MaxUsers → quota.
	_, err = f.svc.Invite(ctx, actorOf(owner), "second@example.mn", domain.RoleOperator)
	require.NoError(t, err)
	_, err = f.svc.Invite(ctx, actorOf(owner), "third@example.mn", domain.RoleOperator)
	require.ErrorIs(t, err, ErrQuota)
	var q *QuotaError
	require.True(t, errors.As(err, &q))
	assert.Equal(t, 3, q.Limit)
	assert.Equal(t, 3, q.Used)

	// Guards.
	_, err = f.svc.Invite(ctx, actorOf(owner), "owner@example.mn", domain.RoleOperator)
	assert.ErrorIs(t, err, domain.ErrConflict)
	_, err = f.svc.Invite(ctx, actorOf(owner), "x@example.mn", "boss")
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.Invite(ctx, actorOf(owner), "bad", domain.RoleAdmin)
	assert.ErrorIs(t, err, domain.ErrInvalid)

	// Accept.
	tok = f.mail.lastToken(t, "op@example.mn", "/accept-invitation")
	_, err = f.svc.AcceptInvitation(ctx, AcceptInvitationInput{Token: tok, Name: "Op", Password: "short"}, client)
	assert.ErrorIs(t, err, domain.ErrInvalid)
	res, err := f.svc.AcceptInvitation(ctx, AcceptInvitationInput{Token: tok, Name: "Op", Password: "password1"}, client)
	require.NoError(t, err)
	assert.Equal(t, domain.RoleAdmin, res.User.Role)
	assert.Equal(t, domain.UserActive, res.User.Status)
	assert.NotNil(t, res.User.EmailVerifiedAt)
	assert.Equal(t, owner.Org.ID, res.Org.ID)
	assert.NotEmpty(t, res.RefreshToken)
	_, err = f.svc.AcceptInvitation(ctx, AcceptInvitationInput{Token: tok, Name: "Op", Password: "password1"}, client)
	assert.ErrorIs(t, err, domain.ErrInvalid, "single use")
	_, err = f.svc.Login(ctx, "op@example.mn", "password1", client)
	require.NoError(t, err)

	// The first (replaced) token is dead.
	users, invs, _ = f.svc.ListMembers(ctx, owner.Org.ID)
	assert.Len(t, users, 2)
	assert.Len(t, invs, 1)

	// Admins cannot invite owners; operators cannot invite.
	admin := actorOf(res)
	_, err = f.svc.Invite(ctx, admin, "o2@example.mn", domain.RoleOwner)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = f.svc.Invite(ctx, Actor{UserID: uuid.New(), OrgID: owner.Org.ID, Role: domain.RoleOperator}, "o3@example.mn", domain.RoleOperator)
	assert.ErrorIs(t, err, domain.ErrForbidden)

	// Expired invitations cannot be accepted and free their seat.
	f.clock.Advance(8 * 24 * time.Hour)
	_, err = f.svc.AcceptInvitation(ctx, AcceptInvitationInput{Token: f.mail.lastToken(t, "second@example.mn", "/accept-invitation"), Name: "S", Password: "password1"}, client)
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.Invite(ctx, actorOf(owner), "third@example.mn", domain.RoleOperator)
	require.NoError(t, err)

	// Cancel.
	_, invs, _ = f.svc.ListMembers(ctx, owner.Org.ID)
	require.Len(t, invs, 1)
	_, err = f.svc.CancelInvitation(ctx, uuid.New(), invs[0].ID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "other org")
	_, err = f.svc.CancelInvitation(ctx, owner.Org.ID, invs[0].ID)
	require.NoError(t, err)
	_, invs, _ = f.svc.ListMembers(ctx, owner.Org.ID)
	assert.Empty(t, invs)
}

func TestUpdateMemberGuards(t *testing.T) {
	f := newFixture(t)
	owner := f.signup(t, "Acme", "owner@example.mn")
	admin := f.addMember(t, owner, "admin@example.mn", domain.RoleAdmin)
	op := f.addMember(t, owner, "op@example.mn", domain.RoleOperator)
	role := func(r domain.Role) *domain.Role { return &r }
	status := func(s domain.UserStatus) *domain.UserStatus { return &s }

	// Self.
	_, err := f.svc.UpdateMember(ctx, actorOf(owner), owner.User.ID, MemberUpdate{Role: role(domain.RoleAdmin)})
	assert.ErrorIs(t, err, domain.ErrForbidden)
	// Admin cannot touch owners or grant owner.
	_, err = f.svc.UpdateMember(ctx, actorOf(admin), owner.User.ID, MemberUpdate{Status: status(domain.UserDisabled)})
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = f.svc.UpdateMember(ctx, actorOf(admin), op.User.ID, MemberUpdate{Role: role(domain.RoleOwner)})
	assert.ErrorIs(t, err, domain.ErrForbidden)
	// Operators cannot manage.
	_, err = f.svc.UpdateMember(ctx, actorOf(op), admin.User.ID, MemberUpdate{Role: role(domain.RoleOperator)})
	assert.ErrorIs(t, err, domain.ErrForbidden)
	// Validation / scoping.
	_, err = f.svc.UpdateMember(ctx, actorOf(owner), op.User.ID, MemberUpdate{Status: status(domain.UserInvited)})
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.UpdateMember(ctx, actorOf(owner), uuid.New(), MemberUpdate{Role: role(domain.RoleAdmin)})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	other := f.signup(t, "Other", "other@example.mn")
	_, err = f.svc.UpdateMember(ctx, actorOf(owner), other.User.ID, MemberUpdate{Role: role(domain.RoleAdmin)})
	assert.ErrorIs(t, err, domain.ErrNotFound, "cross-org")

	// Admin promotes operator to admin; owner promotes admin to owner.
	u, err := f.svc.UpdateMember(ctx, actorOf(admin), op.User.ID, MemberUpdate{Role: role(domain.RoleAdmin)})
	require.NoError(t, err)
	assert.Equal(t, domain.RoleAdmin, u.Role)
	u, err = f.svc.UpdateMember(ctx, actorOf(owner), admin.User.ID, MemberUpdate{Role: role(domain.RoleOwner)})
	require.NoError(t, err)
	assert.Equal(t, domain.RoleOwner, u.Role)
	coOwner := actorOf(admin)
	coOwner.Role = domain.RoleOwner

	// Now two owners: co-owner may demote the original owner…
	_, err = f.svc.UpdateMember(ctx, coOwner, owner.User.ID, MemberUpdate{Role: role(domain.RoleAdmin)})
	require.NoError(t, err)
	// …but the original (now admin) cannot touch the last owner, and the last
	// owner cannot be demoted/disabled by anyone.
	demoted := actorOf(owner)
	demoted.Role = domain.RoleAdmin
	_, err = f.svc.UpdateMember(ctx, demoted, admin.User.ID, MemberUpdate{Role: role(domain.RoleAdmin)})
	assert.ErrorIs(t, err, domain.ErrForbidden)
	third := f.addMember(t, owner, "own3@example.mn", domain.RoleOperator)
	_, err = f.svc.UpdateMember(ctx, coOwner, third.User.ID, MemberUpdate{Role: role(domain.RoleOwner)})
	require.NoError(t, err)
	thirdOwner := actorOf(third)
	thirdOwner.Role = domain.RoleOwner
	_, err = f.svc.UpdateMember(ctx, coOwner, third.User.ID, MemberUpdate{Status: status(domain.UserDisabled)})
	require.NoError(t, err, "another active owner remains")
	_, err = f.svc.UpdateMember(ctx, thirdOwner, admin.User.ID, MemberUpdate{Status: status(domain.UserDisabled)})
	assert.ErrorIs(t, err, domain.ErrConflict, "last active owner")

	// Disabling revokes sessions and blocks login.
	_, err = f.svc.UpdateMember(ctx, coOwner, op.User.ID, MemberUpdate{Status: status(domain.UserDisabled)})
	require.NoError(t, err)
	_, err = f.svc.Refresh(ctx, op.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.Login(ctx, "op@example.mn", "password1", client)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

func TestLastOwnerCannotBeDemotedWhenAlone(t *testing.T) {
	f := newFixture(t)
	owner := f.signup(t, "Acme", "owner@example.mn")
	second := f.addMember(t, owner, "o2@example.mn", domain.RoleOwner)
	status := domain.UserDisabled
	_, err := f.svc.UpdateMember(ctx, actorOf(second), owner.User.ID, MemberUpdate{Status: &status})
	require.NoError(t, err)
	r := domain.RoleAdmin
	// owner is disabled; second is the only active owner and cannot change self;
	// a (hypothetical) admin cannot demote them either.
	_, err = f.svc.UpdateMember(ctx, Actor{UserID: uuid.New(), OrgID: owner.Org.ID, Role: domain.RoleOwner}, second.User.ID, MemberUpdate{Role: &r})
	assert.ErrorIs(t, err, domain.ErrConflict)
	_, err = f.svc.RemoveMember(ctx, Actor{UserID: uuid.New(), OrgID: owner.Org.ID, Role: domain.RoleOwner}, second.User.ID)
	assert.ErrorIs(t, err, domain.ErrConflict)
}

func TestRemoveMember(t *testing.T) {
	f := newFixture(t)
	owner := f.signup(t, "Acme", "owner@example.mn")
	admin := f.addMember(t, owner, "admin@example.mn", domain.RoleAdmin)
	op := f.addMember(t, owner, "op@example.mn", domain.RoleOperator)

	_, err := f.svc.RemoveMember(ctx, actorOf(owner), owner.User.ID)
	assert.ErrorIs(t, err, domain.ErrForbidden, "self")
	_, err = f.svc.RemoveMember(ctx, actorOf(admin), owner.User.ID)
	assert.ErrorIs(t, err, domain.ErrForbidden, "admin removing owner")
	_, err = f.svc.RemoveMember(ctx, actorOf(op), admin.User.ID)
	assert.ErrorIs(t, err, domain.ErrForbidden, "operator")

	_, err = f.svc.RemoveMember(ctx, actorOf(admin), op.User.ID)
	require.NoError(t, err)
	_, err = f.repo.GetUser(ctx, op.User.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.Refresh(ctx, op.RefreshToken, client)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.RemoveMember(ctx, actorOf(admin), op.User.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAPIKeys(t *testing.T) {
	f := newFixture(t)
	owner := f.signup(t, "Acme", "owner@example.mn")

	_, _, err := f.svc.CreateAPIKey(ctx, actorOf(owner), "CI", nil)
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, _, err = f.svc.CreateAPIKey(ctx, actorOf(owner), "CI", []string{"calls:delete"})
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, _, err = f.svc.CreateAPIKey(ctx, actorOf(owner), "", []string{"*"})
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, _, err = f.svc.CreateAPIKey(ctx, Actor{UserID: uuid.New(), OrgID: owner.Org.ID, Role: domain.RoleOperator}, "x", []string{"*"})
	assert.ErrorIs(t, err, domain.ErrForbidden)

	k, plain, err := f.svc.CreateAPIKey(ctx, actorOf(owner), "CRM sync", []string{"calls:read", "contacts:write", "calls:read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"calls:read", "contacts:write"}, k.Scopes)
	assert.Regexp(t, `^cg_live_[a-z0-9]{8}_[A-Za-z0-9]{32}$`, plain)
	prefix, secret, ok := auth.ParseAPIKey(plain)
	require.True(t, ok)
	assert.Equal(t, k.Prefix, prefix)
	assert.Equal(t, HashToken(plain), k.KeyHash)
	assert.NotContains(t, k.KeyHash, secret)
	require.NotNil(t, k.CreatedBy)

	gotKey, org, err := f.svc.Resolve(ctx, prefix, secret)
	require.NoError(t, err)
	assert.Equal(t, k.ID, gotKey.ID)
	assert.Equal(t, owner.Org.ID, org.ID)
	stored, _ := f.repo.GetAPIKeyByPrefix(ctx, prefix)
	require.NotNil(t, stored.LastUsedAt)

	_, _, err = f.svc.Resolve(ctx, prefix, strings.Repeat("A", 32))
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, _, err = f.svc.Resolve(ctx, "zzzzzzzz", secret)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)

	// Feature gate and closed orgs.
	f.svc.HasFeature = func(_ context.Context, _ uuid.UUID, feat string) (bool, error) { return feat != FeatureAPI, nil }
	_, _, err = f.svc.Resolve(ctx, prefix, secret)
	assert.ErrorIs(t, err, auth.ErrFeatureUnavailable)
	f.svc.HasFeature = nil
	o, _ := f.repo.GetOrg(ctx, owner.Org.ID)
	o.Status = domain.OrgClosed
	require.NoError(t, f.repo.UpdateOrg(ctx, o))
	_, _, err = f.svc.Resolve(ctx, prefix, secret)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	o.Status = domain.OrgSuspended
	require.NoError(t, f.repo.UpdateOrg(ctx, o))
	_, _, err = f.svc.Resolve(ctx, prefix, secret)
	require.NoError(t, err, "suspended orgs authenticate; the org gate answers 402")

	keys, err := f.svc.ListAPIKeys(ctx, owner.Org.ID)
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	other := f.signup(t, "Other", "other@example.mn")
	_, err = f.svc.RevokeAPIKey(ctx, other.Org.ID, k.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.RevokeAPIKey(ctx, owner.Org.ID, k.ID)
	require.NoError(t, err)
	_, _, err = f.svc.Resolve(ctx, prefix, secret)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestOrgUpdateAndAudit(t *testing.T) {
	f := newFixture(t)
	owner := f.signup(t, "Acme", "owner@example.mn")
	ov, err := f.svc.GetOrg(ctx, owner.Org.ID)
	require.NoError(t, err)
	require.NotNil(t, ov.Plan)
	require.NotNil(t, ov.Subscription)

	name := "Acme Mongolia"
	tz := "Europe/Berlin"
	o, err := f.svc.UpdateOrg(ctx, actorOf(owner), OrgUpdate{Name: &name, Timezone: &tz, Settings: map[string]any{"recordCalls": false, "contactPhone": nil}})
	require.NoError(t, err)
	assert.Equal(t, name, o.Name)
	assert.Equal(t, tz, o.Timezone)
	assert.Equal(t, map[string]any{"recordCalls": false}, o.Settings)
	bad := "Mars/Olympus"
	_, err = f.svc.UpdateOrg(ctx, actorOf(owner), OrgUpdate{Timezone: &bad})
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.UpdateOrg(ctx, actorOf(owner), OrgUpdate{Settings: map[string]any{"sms": map[string]any{}}})
	assert.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.UpdateOrg(ctx, Actor{OrgID: owner.Org.ID, Role: domain.RoleOperator}, OrgUpdate{Name: &name})
	assert.ErrorIs(t, err, domain.ErrForbidden)

	require.NoError(t, f.svc.Audit(ctx, owner.Org.ID, Actor{UserID: owner.User.ID}, "org.update", "organization", owner.Org.ID.String(), map[string]any{"name": name}, "1.2.3.4"))
	keyID := uuid.New()
	require.NoError(t, f.svc.Audit(ctx, owner.Org.ID, Actor{APIKeyID: &keyID}, "call.dial", "call", "x", nil, ""))
	f.clock.Advance(time.Second)

	items, total, err := f.svc.ListAudit(ctx, owner.Org.ID, domain.AuditFilter{Action: "org.update"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	assert.Equal(t, "owner@example.mn", items[0].ActorEmail, "email resolved from user")
	assert.Equal(t, "1.2.3.4", items[0].IP)
	items, _, err = f.svc.ListAudit(ctx, owner.Org.ID, domain.AuditFilter{Action: "call.dial"})
	require.NoError(t, err)
	assert.Equal(t, "api-key:"+keyID.String(), items[0].ActorEmail)
	assert.Nil(t, items[0].ActorID)
	_, total, err = f.svc.ListAudit(ctx, owner.Org.ID, domain.AuditFilter{ActorID: &owner.User.ID})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, 2) // signup + org.update
}
