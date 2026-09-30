package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var secret = []byte("test-secret")

func TestPassword(t *testing.T) {
	h, err := HashPassword("admin1234")
	require.NoError(t, err)
	assert.True(t, CheckPassword(h, "admin1234"))
	assert.False(t, CheckPassword(h, "wrong"))
	assert.False(t, CheckPassword("", "admin1234"))
	_, err = HashPassword("")
	assert.ErrorIs(t, err, domain.ErrInvalid)
}

func TestTokenRoundTrip(t *testing.T) {
	c := Claims{UserID: uuid.New(), OrgID: uuid.New(), Role: domain.RoleAdmin}
	tok, err := IssueToken(secret, c, time.Hour)
	require.NoError(t, err)
	got, err := ParseToken(secret, tok)
	require.NoError(t, err)
	assert.Equal(t, c, got)

	_, err = ParseToken([]byte("other"), tok)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = ParseToken(secret, tok+"x")
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestTokenExpiredAndAlg(t *testing.T) {
	c := Claims{UserID: uuid.New(), OrgID: uuid.New(), Role: domain.RoleOwner}
	tc := tokenClaims{Org: c.OrgID.String(), Role: "owner", RegisteredClaims: jwt.RegisteredClaims{
		Subject: c.UserID.String(), ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	}}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, tc).SignedString(secret)
	require.NoError(t, err)
	_, err = ParseToken(secret, expired)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)

	tc.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, tc).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	_, err = ParseToken(secret, none)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)

	tc.Role = "root"
	bad, err := jwt.NewWithClaims(jwt.SigningMethodHS256, tc).SignedString(secret)
	require.NoError(t, err)
	_, err = ParseToken(secret, bad)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestMiddleware(t *testing.T) {
	c := Claims{UserID: uuid.New(), OrgID: uuid.New(), Role: domain.RoleOperator}
	tok, err := IssueToken(secret, c, time.Hour)
	require.NoError(t, err)

	var seen Claims
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	h := RequireAuth(secret)(ok)
	admin := RequireAuth(secret)(RequireRole(domain.RoleOwner, domain.RoleAdmin)(ok))

	cases := []struct {
		name   string
		h      http.Handler
		url    string
		header string
		want   int
	}{
		{"no token", h, "/", "", http.StatusUnauthorized},
		{"bad token", h, "/", "Bearer nope", http.StatusUnauthorized},
		{"wrong scheme", h, "/", "Basic " + tok, http.StatusUnauthorized},
		{"bearer", h, "/", "Bearer " + tok, http.StatusNoContent},
		{"query", h, "/?token=" + tok, "", http.StatusNoContent},
		{"role denied", admin, "/", "Bearer " + tok, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			tc.h.ServeHTTP(w, r)
			assert.Equal(t, tc.want, w.Code)
			if tc.want == http.StatusUnauthorized || tc.want == http.StatusForbidden {
				assert.Contains(t, w.Body.String(), `"error"`)
			}
		})
	}
	assert.Equal(t, c, seen)
}

func TestRequireAgentToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, tc := range []struct {
		configured, sent string
		want             int
	}{
		{"s3cret", "s3cret", 200},
		{"s3cret", "nope", 401},
		{"s3cret", "", 401},
		{"", "", 401},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.sent != "" {
			r.Header.Set(AgentTokenHeader, tc.sent)
		}
		w := httptest.NewRecorder()
		RequireAgentToken(tc.configured)(ok).ServeHTTP(w, r)
		assert.Equal(t, tc.want, w.Code, "%+v", tc)
	}
}
