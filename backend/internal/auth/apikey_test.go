package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	testPrefix = "ab12cd34"
	testSecret = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"
)

func TestParseAPIKey(t *testing.T) {
	key := FormatAPIKey(testPrefix, testSecret)
	assert.Equal(t, "cg_live_ab12cd34_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef", key)
	p, s, ok := ParseAPIKey(key)
	require.True(t, ok)
	assert.Equal(t, testPrefix, p)
	assert.Equal(t, testSecret, s)
	for _, bad := range []string{
		"", "cg_live_", "cg_test_ab12cd34_" + testSecret, "cg_live_ab12cd3_" + testSecret,
		"cg_live_AB12CD34_" + testSecret, "cg_live_ab12cd34_" + testSecret[:31], "cg_live_ab12cd34_" + testSecret[:31] + "!",
		"cg_live_ab12cd34" + testSecret,
	} {
		_, _, ok := ParseAPIKey(bad)
		assert.False(t, ok, bad)
	}
}

func TestClaimsHasScope(t *testing.T) {
	id := uuid.New()
	user := Claims{UserID: uuid.New(), Role: domain.RoleOperator}
	assert.True(t, user.HasScope("calls:write"))
	key := Claims{APIKeyID: &id, Scopes: []string{"calls:write", "contacts:read"}}
	assert.True(t, key.HasScope("calls:write"))
	assert.True(t, key.HasScope("calls:read"), "write implies read")
	assert.True(t, key.HasScope("contacts:read"))
	assert.False(t, key.HasScope("contacts:write"))
	assert.False(t, key.HasScope("campaigns:write"))
	star := Claims{APIKeyID: &id, Scopes: []string{"*"}}
	assert.True(t, star.HasScope("knowledge:write"))
}

type stubResolver struct {
	key   *domain.APIKey
	org   *domain.Organization
	err   error
	calls int
}

func (s *stubResolver) Resolve(_ context.Context, prefix, secret string) (*domain.APIKey, *domain.Organization, error) {
	s.calls++
	if s.err != nil {
		return nil, nil, s.err
	}
	if prefix != testPrefix || secret != testSecret {
		return nil, nil, domain.ErrUnauthorized
	}
	return s.key, s.org, nil
}

func TestRequireAuthAPIKey(t *testing.T) {
	orgID := uuid.New()
	keyID := uuid.New()
	res := &stubResolver{
		key: &domain.APIKey{ID: keyID, OrgID: orgID, Prefix: testPrefix, Scopes: []string{"calls:read"}},
		org: &domain.Organization{ID: orgID, Status: domain.OrgActive},
	}
	var seen Claims
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	withKeys := RequireAuth(secret, WithAPIKeys(res))
	withoutKeys := RequireAuth(secret)
	good := "Bearer " + FormatAPIKey(testPrefix, testSecret)

	do := func(h http.Handler, url, header string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := do(withKeys(ok), "/", good)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.NotNil(t, seen.APIKeyID)
	assert.Equal(t, keyID, *seen.APIKeyID)
	assert.Equal(t, orgID, seen.OrgID)
	assert.Equal(t, domain.RoleAdmin, seen.Role)
	assert.Equal(t, uuid.Nil, seen.UserID)
	assert.Equal(t, []string{"calls:read"}, seen.Scopes)
	assert.True(t, seen.IsAPIKey())

	assert.Equal(t, http.StatusUnauthorized, do(withKeys(ok), "/", "Bearer "+FormatAPIKey(testPrefix, strings.Repeat("x", 32))).Code)
	assert.Equal(t, http.StatusUnauthorized, do(withKeys(ok), "/", "Bearer cg_live_garbage").Code)
	assert.Equal(t, http.StatusUnauthorized, do(withoutKeys(ok), "/", good).Code, "API keys disabled without resolver")
	calls := res.calls
	assert.Equal(t, http.StatusUnauthorized, do(withKeys(ok), "/?token="+FormatAPIKey(testPrefix, testSecret), "").Code, "never from query")
	assert.Equal(t, calls, res.calls)

	// JWTs keep working alongside API keys.
	c := Claims{UserID: uuid.New(), OrgID: orgID, Role: domain.RoleOwner}
	tok, err := IssueToken(secret, c, time.Minute)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, do(withKeys(ok), "/", "Bearer "+tok).Code)
	assert.Equal(t, c, seen)

	for _, tc := range []struct {
		err  error
		want int
		code string
	}{
		{domain.ErrForbidden, http.StatusForbidden, "forbidden"},
		{ErrFeatureUnavailable, http.StatusForbidden, "feature_unavailable"},
		{errors.New("db down"), http.StatusInternalServerError, "internal"},
		{domain.ErrNotFound, http.StatusUnauthorized, "unauthorized"},
	} {
		res.err = tc.err
		w := do(withKeys(ok), "/", good)
		assert.Equal(t, tc.want, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"`+tc.code+`"`)
	}
}

func TestRequireScopeAndUser(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	id := uuid.New()
	run := func(h http.Handler, c *Claims) int {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if c != nil {
			r = r.WithContext(WithClaims(r.Context(), *c))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	scoped := RequireScope("campaigns:write")(ok)
	user := &Claims{UserID: uuid.New(), Role: domain.RoleOperator}
	assert.Equal(t, http.StatusNoContent, run(scoped, user))
	assert.Equal(t, http.StatusNoContent, run(scoped, &Claims{APIKeyID: &id, Role: domain.RoleAdmin, Scopes: []string{"campaigns:write"}}))
	assert.Equal(t, http.StatusNoContent, run(scoped, &Claims{APIKeyID: &id, Role: domain.RoleAdmin, Scopes: []string{"*"}}))
	assert.Equal(t, http.StatusForbidden, run(scoped, &Claims{APIKeyID: &id, Role: domain.RoleAdmin, Scopes: []string{"calls:read"}}))
	assert.Equal(t, http.StatusUnauthorized, run(scoped, nil))

	assert.Equal(t, http.StatusNoContent, run(RequireUser(ok), user))
	assert.Equal(t, http.StatusForbidden, run(RequireUser(ok), &Claims{APIKeyID: &id, Scopes: []string{"*"}}))
	assert.Equal(t, http.StatusUnauthorized, run(RequireUser(ok), nil))
}

func TestRequirePlatformAdmin(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	staff := Claims{UserID: uuid.New(), OrgID: uuid.New(), Role: domain.RoleOwner, PlatformAdmin: true}
	tok, err := IssueToken(secret, staff, time.Minute)
	require.NoError(t, err)
	parsed, err := ParseToken(secret, tok)
	require.NoError(t, err)
	assert.True(t, parsed.PlatformAdmin, "pa claim round-trips")

	run := func(h http.Handler, c *Claims) int {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if c != nil {
			r = r.WithContext(WithClaims(r.Context(), *c))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	byClaim := RequirePlatformAdmin(nil)(ok)
	assert.Equal(t, http.StatusNoContent, run(byClaim, &parsed))
	assert.Equal(t, http.StatusForbidden, run(byClaim, &Claims{UserID: uuid.New(), Role: domain.RoleOwner}))
	assert.Equal(t, http.StatusUnauthorized, run(byClaim, nil))
	id := uuid.New()
	assert.Equal(t, http.StatusForbidden, run(byClaim, &Claims{APIKeyID: &id, PlatformAdmin: true}))

	// A live check overrides the (possibly stale) claim.
	revoked := RequirePlatformAdmin(func(context.Context, uuid.UUID) (bool, error) { return false, nil })(ok)
	assert.Equal(t, http.StatusForbidden, run(revoked, &parsed))
	granted := RequirePlatformAdmin(func(_ context.Context, uid uuid.UUID) (bool, error) { return uid == staff.UserID, nil })(ok)
	assert.Equal(t, http.StatusNoContent, run(granted, &Claims{UserID: staff.UserID, Role: domain.RoleOwner}))
	failing := RequirePlatformAdmin(func(context.Context, uuid.UUID) (bool, error) { return false, errors.New("db") })(ok)
	assert.Equal(t, http.StatusInternalServerError, run(failing, &parsed))
}

func TestIssueTokenRejectsAPIKeyClaimsAndDefaultsTTL(t *testing.T) {
	id := uuid.New()
	_, err := IssueToken(secret, Claims{UserID: uuid.New(), OrgID: uuid.New(), Role: domain.RoleAdmin, APIKeyID: &id}, time.Minute)
	assert.Error(t, err)
	assert.Equal(t, 15*time.Minute, DefaultTokenTTL)
}
