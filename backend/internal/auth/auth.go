// Package auth implements password hashing, JWT issuing/parsing and the chi
// middlewares that protect the CallGo.mn HTTP API.
//
// Tokens are HS256 JWTs with claims {sub: userId, org: orgId, role}.
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

// DefaultTokenTTL is the lifetime of dashboard tokens.
const DefaultTokenTTL = 24 * time.Hour

// maxPasswordBytes is bcrypt's input limit.
const maxPasswordBytes = 72

// Claims is the authenticated identity carried by a token.
type Claims struct {
	UserID uuid.UUID   `json:"userId"`
	OrgID  uuid.UUID   `json:"orgId"`
	Role   domain.Role `json:"role"`
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
	Org  string `json:"org"`
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// IssueToken signs claims into an HS256 JWT valid for ttl.
func IssueToken(secret []byte, c Claims, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("issue token: empty secret")
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	now := time.Now()
	tc := tokenClaims{
		Org:  c.OrgID.String(),
		Role: string(c.Role),
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
	return Claims{UserID: uid, OrgID: oid, Role: role}, nil
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
// the request context.
func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := TokenFromRequest(r)
			if tok == "" {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
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
