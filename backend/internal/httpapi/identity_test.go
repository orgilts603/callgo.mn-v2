package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestIdentitySignupLoginRefreshLogout(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})

	w := e.do(http.MethodPost, "/api/auth/signup", "", map[string]any{"orgName": "Номин Трейд", "email": "Owner@Example.mn", "password": "password1", "name": "Бат", "phone": "+97699112233"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	s := decode[idnAuthResp](t, w)
	assert.NotEmpty(t, s.Token)
	assert.NotEmpty(t, s.RefreshToken)
	assert.Equal(t, "owner@example.mn", s.User.Email)
	assert.Equal(t, domain.RoleOwner, s.User.Role)
	assert.Equal(t, "nomin-treid", s.Org.Slug)
	assert.Equal(t, "trial", s.Org.PlanCode)
	require.NotNil(t, s.Subscription)
	assert.Equal(t, domain.SubTrialing, s.Subscription.Status)
	assert.NotContains(t, w.Body.String(), "passwordHash")
	assert.Equal(t, 1, e.mail.count(), "verification email")

	requireErr(t, e.do(http.MethodPost, "/api/auth/signup", "", map[string]any{"orgName": "X", "email": "owner@example.mn", "password": "password1", "name": "X"}), http.StatusConflict, "conflict")
	requireErr(t, e.do(http.MethodPost, "/api/auth/signup", "", map[string]any{"orgName": "X", "email": "x@example.mn", "password": "short", "name": "X"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/auth/signup", "", nil), http.StatusBadRequest, "invalid")

	// Login returns refreshToken + subscription.
	requireErr(t, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "nope-nope"}), http.StatusUnauthorized, "unauthorized")
	w = e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "password1"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	for _, k := range []string{"token", "refreshToken", "user", "org", "subscription"} {
		assert.Contains(t, raw, k)
	}
	l := decode[idnAuthResp](t, w)
	c, err := auth.ParseToken(identityTestJWT, l.Token)
	require.NoError(t, err)
	assert.Equal(t, s.User.ID, c.UserID)

	// Refresh rotates; reusing the old token is rejected and kills all sessions.
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{}), http.StatusBadRequest, "invalid")
	w = e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": l.RefreshToken})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r1 := decode[idnAuthResp](t, w)
	assert.NotEqual(t, l.RefreshToken, r1.RefreshToken)
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": l.RefreshToken}), http.StatusUnauthorized, "unauthorized")
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": r1.RefreshToken}), http.StatusUnauthorized, "unauthorized")
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": s.RefreshToken}), http.StatusUnauthorized, "unauthorized")

	// Logout.
	l2 := decode[idnAuthResp](t, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "password1"}))
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/logout", "", map[string]any{"refreshToken": l2.RefreshToken}).Code)
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/logout", "", nil).Code)
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": l2.RefreshToken}), http.StatusUnauthorized, "unauthorized")

	// Closed orgs cannot log in.
	org, _ := e.repo.GetOrg(context.Background(), s.Org.ID)
	org.Status = domain.OrgClosed
	require.NoError(t, e.repo.UpdateOrg(context.Background(), org))
	requireErr(t, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "password1"}), http.StatusForbidden, "forbidden")
}

func TestIdentitySignupDisabled(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: false})
	requireErr(t, e.do(http.MethodPost, "/api/auth/signup", "", map[string]any{"orgName": "X", "email": "x@example.mn", "password": "password1", "name": "X"}), http.StatusForbidden, "forbidden")
}

func TestIdentityRateLimit(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true, rateLimit: true})
	for i := range 5 {
		w := e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "ghost@example.mn", "password": "password1"})
		require.Equal(t, http.StatusUnauthorized, w.Code, "attempt %d", i+1)
	}
	requireErr(t, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "ghost@example.mn", "password": "password1"}), http.StatusTooManyRequests, "rate_limited")
	// Limits are per route: forgot-password still has its own budget.
	for range 5 {
		assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/forgot-password", "", map[string]any{"email": "x@example.mn"}).Code)
	}
	requireErr(t, e.do(http.MethodPost, "/api/auth/forgot-password", "", map[string]any{"email": "x@example.mn"}), http.StatusTooManyRequests, "rate_limited")
	// Refresh is not rate limited here.
	for range 7 {
		assert.Equal(t, http.StatusUnauthorized, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": "x"}).Code)
	}
}

func TestIdentityVerifyForgotReset(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")

	requireErr(t, e.do(http.MethodPost, "/api/auth/verify-email", "", map[string]any{"token": "bogus"}), http.StatusBadRequest, "invalid")
	n := e.mail.count()
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/resend-verification", s.Token, nil).Code)
	assert.Equal(t, n+1, e.mail.count(), "resend for the signed-in user")
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/resend-verification", "", map[string]any{"email": "ghost@example.mn"}).Code)
	assert.Equal(t, n+1, e.mail.count())

	w := e.do(http.MethodPost, "/api/auth/verify-email", "", map[string]any{"token": e.mail.token(t, "owner@example.mn", "/verify-email")})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	v := decode[struct{ User domain.User }](t, w)
	assert.NotNil(t, v.User.EmailVerifiedAt)

	// Forgot: always 204.
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/forgot-password", "", map[string]any{"email": "ghost@example.mn"}).Code)
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/forgot-password", "", map[string]any{"email": "OWNER@example.mn"}).Code)
	tok := e.mail.token(t, "owner@example.mn", "/reset-password")
	requireErr(t, e.do(http.MethodPost, "/api/auth/reset-password", "", map[string]any{"token": "bad", "password": "newpassword1"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/auth/reset-password", "", map[string]any{"token": tok, "password": "short"}), http.StatusBadRequest, "invalid")
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/reset-password", "", map[string]any{"token": tok, "password": "newpassword1"}).Code)
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": s.RefreshToken}), http.StatusUnauthorized, "unauthorized")
	assert.Equal(t, http.StatusOK, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "newpassword1"}).Code)
	assert.Contains(t, e.repo.Actions(), "auth.password_reset")
}

func TestIdentitySessionsAndChangePassword(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")
	phone := decode[idnAuthResp](t, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "password1"}))

	requireErr(t, e.do(http.MethodGet, "/api/auth/sessions", "", nil), http.StatusUnauthorized, "unauthorized")
	w := e.do(http.MethodGet, "/api/auth/sessions", s.Token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	sessions := decode[list[domain.RefreshSession]](t, w)
	require.Len(t, sessions.Items, 2)
	assert.Equal(t, "203.0.113.9", sessions.Items[0].IP)
	assert.NotContains(t, w.Body.String(), "tokenHash")

	requireErr(t, e.do(http.MethodDelete, "/api/auth/sessions/"+uuid.NewString(), s.Token, nil), http.StatusNotFound, "not_found")
	requireErr(t, e.do(http.MethodDelete, "/api/auth/sessions/nope", s.Token, nil), http.StatusBadRequest, "invalid")

	requireErr(t, e.do(http.MethodPost, "/api/auth/change-password", s.Token, map[string]any{"currentPassword": "wrong-pass", "newPassword": "newpassword1"}), http.StatusBadRequest, "invalid")
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodPost, "/api/auth/change-password", s.Token,
		map[string]any{"currentPassword": "password1", "newPassword": "newpassword1", "refreshToken": s.RefreshToken}).Code)
	requireErr(t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": phone.RefreshToken}), http.StatusUnauthorized, "unauthorized")
	w = e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": s.RefreshToken})
	require.Equal(t, http.StatusOK, w.Code, "caller's session survives")

	sessions = decode[list[domain.RefreshSession]](t, e.do(http.MethodGet, "/api/auth/sessions", s.Token, nil))
	require.Len(t, sessions.Items, 1)
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/auth/sessions/"+sessions.Items[0].ID.String(), s.Token, nil).Code)
	sessions = decode[list[domain.RefreshSession]](t, e.do(http.MethodGet, "/api/auth/sessions", s.Token, nil))
	assert.Empty(t, sessions.Items)
	assert.Contains(t, e.repo.Actions(), "auth.password_change")
	assert.Contains(t, e.repo.Actions(), "session.revoke")
}

func TestIdentityOrg(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")
	op := e.member(s, "op@example.mn", domain.RoleOperator)

	w := e.do(http.MethodGet, "/api/org", op.Token, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	ov := decode[struct {
		Org          domain.Organization  `json:"org"`
		Subscription *domain.Subscription `json:"subscription"`
		Plan         *domain.Plan         `json:"plan"`
	}](t, w)
	assert.Equal(t, s.Org.ID, ov.Org.ID)
	require.NotNil(t, ov.Subscription)
	require.NotNil(t, ov.Plan)
	assert.Equal(t, "trial", ov.Plan.Code)

	requireErr(t, e.do(http.MethodPut, "/api/org", op.Token, map[string]any{"name": "Hacked"}), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPut, "/api/org", s.Token, map[string]any{"timezone": "Nowhere/City"}), http.StatusBadRequest, "invalid")
	w = e.do(http.MethodPut, "/api/org", s.Token, map[string]any{"name": "Acme MN", "timezone": "Asia/Hovd", "settings": map[string]any{"recordCalls": false}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	o := decode[struct{ Org domain.Organization }](t, w).Org
	assert.Equal(t, "Acme MN", o.Name)
	assert.Equal(t, "Asia/Hovd", o.Timezone)
	assert.Equal(t, false, o.Settings["recordCalls"])
	assert.Contains(t, e.repo.Actions(), "org.update")
}

func TestIdentityMembersAndInvitations(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")
	op := e.member(s, "op@example.mn", domain.RoleOperator)
	e.maxUsers = 3 // owner + op = 2 users

	requireErr(t, e.do(http.MethodGet, "/api/org/members", op.Token, nil), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPost, "/api/org/invitations", op.Token, map[string]any{"email": "z@example.mn", "role": "operator"}), http.StatusForbidden, "forbidden")

	w := e.do(http.MethodPost, "/api/org/invitations", s.Token, map[string]any{"email": "new@example.mn", "role": "admin"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	inv := decode[struct{ Invitation domain.Invitation }](t, w).Invitation
	assert.Equal(t, "new@example.mn", inv.Email)
	assert.NotContains(t, w.Body.String(), "tokenHash")

	// Quota: 2 users + 1 pending = 3.
	w = e.do(http.MethodPost, "/api/org/invitations", s.Token, map[string]any{"email": "more@example.mn", "role": "operator"})
	requireErr(t, w, http.StatusTooManyRequests, "quota_exceeded")
	var qb struct {
		Error struct {
			Details struct{ Limit, Used int } `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &qb))
	assert.Equal(t, 3, qb.Error.Details.Limit)
	assert.Equal(t, 3, qb.Error.Details.Used)

	requireErr(t, e.do(http.MethodPost, "/api/org/invitations", s.Token, map[string]any{"email": "op@example.mn", "role": "operator"}), http.StatusConflict, "conflict")
	requireErr(t, e.do(http.MethodPost, "/api/org/invitations", s.Token, map[string]any{"email": "bad", "role": "operator"}), http.StatusBadRequest, "invalid")

	w = e.do(http.MethodGet, "/api/org/members", s.Token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	members := decode[struct {
		Items       []domain.User       `json:"items"`
		Invitations []domain.Invitation `json:"invitations"`
	}](t, w)
	assert.Len(t, members.Items, 2)
	require.Len(t, members.Invitations, 1)

	// Cancel the invitation; unknown → 404.
	requireErr(t, e.do(http.MethodDelete, "/api/org/invitations/"+uuid.NewString(), s.Token, nil), http.StatusNotFound, "not_found")
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/org/invitations/"+inv.ID.String(), s.Token, nil).Code)
	requireErr(t, e.do(http.MethodPost, "/api/auth/accept-invitation", "", map[string]any{"token": e.mail.token(t, "new@example.mn", "/accept-invitation"), "name": "N", "password": "password1"}), http.StatusBadRequest, "invalid")

	// Update member: self and last-owner guards.
	requireErr(t, e.do(http.MethodPut, "/api/org/members/"+s.User.ID.String(), s.Token, map[string]any{"role": "admin"}), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPut, "/api/org/members/"+op.User.ID.String(), s.Token, map[string]any{}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPut, "/api/org/members/"+uuid.NewString(), s.Token, map[string]any{"role": "admin"}), http.StatusNotFound, "not_found")
	w = e.do(http.MethodPut, "/api/org/members/"+op.User.ID.String(), s.Token, map[string]any{"role": "owner"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, domain.RoleOwner, decode[struct{ User domain.User }](t, w).User.Role)
	// op is now an owner (fresh token carries the new role after refresh).
	op2 := decode[idnAuthResp](t, e.do(http.MethodPost, "/api/auth/refresh", "", map[string]any{"refreshToken": op.RefreshToken}))
	assert.Equal(t, http.StatusOK, e.do(http.MethodPut, "/api/org/members/"+s.User.ID.String(), op2.Token, map[string]any{"role": "admin"}).Code)
	requireErr(t, e.do(http.MethodPut, "/api/org/members/"+op.User.ID.String(), e.freshToken("owner@example.mn"), map[string]any{"status": "disabled"}),
		http.StatusForbidden, "forbidden") // admins cannot touch owners
	// Remove.
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/org/members/"+s.User.ID.String(), op2.Token, nil).Code)
	requireErr(t, e.do(http.MethodDelete, "/api/org/members/"+op.User.ID.String(), op2.Token, nil), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "password1"}), http.StatusUnauthorized, "unauthorized")

	actions := e.repo.Actions()
	for _, a := range []string{"invitation.create", "invitation.delete", "member.update", "member.remove", "invitation.accept"} {
		assert.Contains(t, actions, a)
	}
}

// freshToken logs in and returns a new access token (role changes apply).
func (e *identityEnv) freshToken(email string) string {
	e.t.Helper()
	w := e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": email, "password": "password1"})
	require.Equal(e.t, http.StatusOK, w.Code, w.Body.String())
	return decode[idnAuthResp](e.t, w).Token
}

func TestIdentityAudit(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")
	op := e.member(s, "op@example.mn", domain.RoleOperator)
	requireErr(t, e.do(http.MethodGet, "/api/org/audit", op.Token, nil), http.StatusForbidden, "forbidden")

	w := e.do(http.MethodGet, "/api/org/audit?action=invitation.create", s.Token, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res := decode[list[domain.AuditEntry]](t, w)
	require.Equal(t, 1, res.Total)
	assert.Equal(t, "owner@example.mn", res.Items[0].ActorEmail)
	assert.Equal(t, "203.0.113.9", res.Items[0].IP)
	assert.Equal(t, "op@example.mn", res.Items[0].Meta["email"])

	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	w = e.do(http.MethodGet, "/api/org/audit?limit=2&actorId="+s.User.ID.String()+"&from="+from, s.Token, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res = decode[list[domain.AuditEntry]](t, w)
	assert.Len(t, res.Items, 2)
	assert.GreaterOrEqual(t, res.Total, 2)
	requireErr(t, e.do(http.MethodGet, "/api/org/audit?from=yesterday", s.Token, nil), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodGet, "/api/org/audit?limit=0", s.Token, nil), http.StatusBadRequest, "invalid")
}

func TestAPIKeyEndpointsAndAuth(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")
	op := e.member(s, "op@example.mn", domain.RoleOperator)

	requireErr(t, e.do(http.MethodGet, "/api/org/api-keys", op.Token, nil), http.StatusForbidden, "forbidden")
	e.features["api"] = false
	requireErr(t, e.do(http.MethodGet, "/api/org/api-keys", s.Token, nil), http.StatusForbidden, "feature_unavailable")
	requireErr(t, e.do(http.MethodPost, "/api/org/api-keys", s.Token, map[string]any{"name": "k", "scopes": []string{"*"}}), http.StatusForbidden, "feature_unavailable")
	e.features["api"] = true

	requireErr(t, e.do(http.MethodPost, "/api/org/api-keys", s.Token, map[string]any{"name": "k", "scopes": []string{"root"}}), http.StatusBadRequest, "invalid")
	w := e.do(http.MethodPost, "/api/org/api-keys", s.Token, map[string]any{"name": "CRM", "scopes": []string{"calls:read"}})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	created := decode[struct {
		APIKey    domain.APIKey `json:"apiKey"`
		Plaintext string        `json:"plaintext"`
	}](t, w)
	assert.Regexp(t, `^cg_live_[a-z0-9]{8}_[A-Za-z0-9]{32}$`, created.Plaintext)
	assert.NotContains(t, w.Body.String(), "keyHash")

	w = e.do(http.MethodGet, "/api/org/api-keys", s.Token, nil)
	require.Equal(t, http.StatusOK, w.Code)
	keys := decode[list[domain.APIKey]](t, w)
	require.Len(t, keys.Items, 1)
	assert.Equal(t, created.APIKey.Prefix, keys.Items[0].Prefix)

	// The key authenticates on API routes with scope checks…
	key := created.Plaintext
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/api/calls", key, nil).Code)
	requireErr(t, e.do(http.MethodPost, "/api/calls/dial", key, nil), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodGet, "/api/calls", key[:len(key)-1]+"X", nil), http.StatusUnauthorized, "unauthorized")
	// …but never on account management routes.
	requireErr(t, e.do(http.MethodGet, "/api/org/api-keys", key, nil), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodGet, "/api/auth/sessions", key, nil), http.StatusForbidden, "forbidden")

	// Plan downgrade: resolution answers feature_unavailable, revoke still works.
	e.features["api"] = false
	requireErr(t, e.do(http.MethodGet, "/api/calls", key, nil), http.StatusForbidden, "feature_unavailable")
	requireErr(t, e.do(http.MethodDelete, "/api/org/api-keys/"+uuid.NewString(), s.Token, nil), http.StatusNotFound, "not_found")
	assert.Equal(t, http.StatusNoContent, e.do(http.MethodDelete, "/api/org/api-keys/"+created.APIKey.ID.String(), s.Token, nil).Code)
	e.features["api"] = true
	requireErr(t, e.do(http.MethodGet, "/api/calls", key, nil), http.StatusUnauthorized, "unauthorized")

	actions := e.repo.Actions()
	assert.Contains(t, actions, "api_key.create")
	assert.Contains(t, actions, "api_key.revoke")
}

func TestTenancySuspendedOrgGate(t *testing.T) {
	e := newIdentityEnv(t, identityEnvOpts{allowSignup: true})
	s := e.signup("Acme", "owner@example.mn")
	w := e.do(http.MethodPost, "/api/org/api-keys", s.Token, map[string]any{"name": "CI", "scopes": []string{"*"}})
	require.Equal(t, http.StatusCreated, w.Code)
	key := decode[struct {
		Plaintext string `json:"plaintext"`
	}](t, w).Plaintext

	ctx := context.Background()
	org, _ := e.repo.GetOrg(ctx, s.Org.ID)
	org.Status = domain.OrgSuspended
	require.NoError(t, e.repo.UpdateOrg(ctx, org))

	// Reads and auth/org/billing stay available; mutations elsewhere → 402.
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/api/calls", s.Token, nil).Code)
	requireErr(t, e.do(http.MethodPost, "/api/calls/dial", s.Token, nil), http.StatusPaymentRequired, "payment_required")
	requireErr(t, e.do(http.MethodPost, "/api/calls/dial", key, nil), http.StatusPaymentRequired, "payment_required")
	w = e.do(http.MethodPost, "/api/auth/login", "", map[string]any{"email": "owner@example.mn", "password": "password1"})
	require.Equal(t, http.StatusOK, w.Code, "suspended orgs can log in to pay")
	assert.Equal(t, http.StatusOK, e.do(http.MethodGet, "/api/org", s.Token, nil).Code)

	org.Status = domain.OrgClosed
	require.NoError(t, e.repo.UpdateOrg(ctx, org))
	requireErr(t, e.do(http.MethodGet, "/api/calls", s.Token, nil), http.StatusForbidden, "forbidden")
	requireErr(t, e.do(http.MethodGet, "/api/calls", key, nil), http.StatusForbidden, "forbidden")

	org.Status = domain.OrgActive
	require.NoError(t, e.repo.UpdateOrg(ctx, org))
	assert.Equal(t, http.StatusOK, e.do(http.MethodPost, "/api/calls/dial", s.Token, nil).Code)
}
