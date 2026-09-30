// Package auth implements password hashing, JWT issuing/parsing and the chi
// middlewares that protect the CallGo.mn HTTP API.
//
// Tokens are HS256 JWTs with claims {sub: userId, org: orgId, role} (plus
// "pa": true for platform staff). RequireAuth also accepts org API keys
// ("cg_live_<prefix>_<secret>") when configured with WithAPIKeys.
package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// AgentTokenHeader is the header the Python agent worker sends on internal calls.
const AgentTokenHeader = "X-Agent-Token"

// DefaultTokenTTL is the lifetime of dashboard access tokens. Sessions are
// kept alive with rotated refresh tokens (DefaultRefreshTTL). Tokens issued
// earlier with a longer lifetime stay valid until they expire.
const DefaultTokenTTL = 15 * time.Minute

// DefaultRefreshTTL is the lifetime of a refresh session (rotated on use).
const DefaultRefreshTTL = 30 * 24 * time.Hour

// maxPasswordBytes is bcrypt's input limit.
const maxPasswordBytes = 72

// Claims is the authenticated identity carried by a token.
//
// For API-key requests UserID is uuid.Nil, Role is admin, APIKeyID is set
// and Scopes lists the key's scopes. For users APIKeyID is nil and Scopes
// is empty (users are not scope-restricted).
type Claims struct {
	UserID uuid.UUID   `json:"userId"`
	OrgID  uuid.UUID   `json:"orgId"`
	Role   domain.Role `json:"role"`
	// APIKeyID is set when the request authenticated with an API key.
	APIKeyID *uuid.UUID `json:"apiKeyId,omitempty"`
	// Scopes of the API key ("calls:read", …, or "*").
	Scopes []string `json:"scopes,omitempty"`
	// PlatformAdmin marks CallGo staff (user.isPlatformAdmin) — JWT only.
	PlatformAdmin bool `json:"platformAdmin,omitempty"`
}

// IsAPIKey reports whether the claims come from an API key.
func (c Claims) IsAPIKey() bool { return c.APIKeyID != nil }

// HasScope reports whether c may use scope. Users always may; API keys need
// the scope itself, "*", or — for "<resource>:read" — "<resource>:write".
func (c Claims) HasScope(scope string) bool {
	if !c.IsAPIKey() {
		return true
	}
	res, action, _ := strings.Cut(scope, ":")
	for _, s := range c.Scopes {
		if s == "*" || s == scope {
			return true
		}
		if action == "read" && s == res+":write" {
			return true
		}
	}
	return false
}

// HashPassword returns a bcrypt hash of password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("empty password: %w", domain.ErrInvalid)
	}
	if len(password) > maxPasswordBytes {
		return "", fmt.Errorf("password longer than %d bytes: %w", maxPasswordBytes, domain.ErrInvalid)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches the bcrypt hash.
func CheckPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type tokenClaims struct {
	Org           string `json:"org"`
	Role          string `json:"role"`
	PlatformAdmin bool   `json:"pa,omitempty"`
	jwt.RegisteredClaims
}

// IssueToken signs claims into an HS256 JWT valid for ttl (DefaultTokenTTL
// when ttl <= 0). API-key claims cannot be issued as tokens.
func IssueToken(secret []byte, c Claims, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("issue token: empty secret")
	}
	if c.IsAPIKey() {
		return "", errors.New("issue token: API-key claims cannot be issued as a JWT")
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	now := time.Now()
	tc := tokenClaims{
		Org:           c.OrgID.String(),
		Role:          string(c.Role),
		PlatformAdmin: c.PlatformAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.UserID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "callgo",
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, tc).SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return s, nil
}

// ParseToken verifies token and returns its claims. Any failure wraps
// domain.ErrUnauthorized.
func ParseToken(secret []byte, token string) (Claims, error) {
	if len(secret) == 0 {
		return Claims{}, fmt.Errorf("parse token: empty secret: %w", domain.ErrUnauthorized)
	}
	var tc tokenClaims
	_, err := jwt.ParseWithClaims(token, &tc, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("parse token: %v: %w", err, domain.ErrUnauthorized)
	}
	uid, err := uuid.Parse(tc.Subject)
	if err != nil {
		return Claims{}, fmt.Errorf("parse token: bad subject: %w", domain.ErrUnauthorized)
	}
	oid, err := uuid.Parse(tc.Org)
	if err != nil {
		return Claims{}, fmt.Errorf("parse token: bad org: %w", domain.ErrUnauthorized)
	}
	role := domain.Role(tc.Role)
	switch role {
	case domain.RoleOwner, domain.RoleAdmin, domain.RoleOperator:
	default:
		return Claims{}, fmt.Errorf("parse token: bad role %q: %w", tc.Role, domain.ErrUnauthorized)
	}
	return Claims{UserID: uid, OrgID: oid, Role: role, PlatformAdmin: tc.PlatformAdmin}, nil
}

type ctxKey struct{}

// WithClaims returns a context carrying c.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns the claims placed by RequireAuth.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}

// TokenFromRequest extracts a bearer token from the Authorization header or,
// failing that, the "token" query parameter (browsers cannot set headers on
// WebSocket upgrades).
func TokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, tok, ok := strings.Cut(h, " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(tok)
		}
		return ""
	}
	return r.URL.Query().Get("token")
}

// RequireAuth rejects requests without a valid token and stores the Claims in
// the request context. With WithAPIKeys it also accepts
// "Authorization: Bearer cg_live_<prefix>_<secret>" (header only, never the
// query string).
func RequireAuth(secret []byte, opts ...Option) func(http.Handler) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if key, ok := apiKeyFromHeader(r); ok {
				c, status, code, msg := o.resolveAPIKey(r, key)
				if status != 0 {
					WriteError(w, status, code, msg)
					return
				}
				next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), c)))
				return
			}
			tok := TokenFromRequest(r)
			if tok == "" {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}
			if strings.HasPrefix(tok, APIKeyPrefix) {
				// API keys are accepted from the Authorization header only.
				WriteError(w, http.StatusUnauthorized, "unauthorized", "API keys must be sent in the Authorization header")
				return
			}
			c, err := ParseToken(secret, tok)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), c)))
		})
	}
}

// RequireRole allows only the given roles. It must run after RequireAuth.
func RequireRole(roles ...domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := FromContext(r.Context())
			if !ok {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
				return
			}
			if !slices.Contains(roles, c.Role) {
				WriteError(w, http.StatusForbidden, "forbidden", "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAgentToken protects the internal agent API with a shared secret in
// the X-Agent-Token header. An empty configured token rejects everything.
func RequireAgentToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get(AgentTokenHeader)
			if token == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteError writes the API error envelope {"error": {"code", "message"}}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
