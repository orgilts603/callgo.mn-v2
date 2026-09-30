package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// API key format: "cg_live_<prefix>_<secret>" with an 8-character lowercase
// alphanumeric prefix (stored in clear for lookup and display) and a
// 32-character alphanumeric secret (only its hash is stored).
const (
	APIKeyPrefix    = "cg_live_"
	APIKeyPrefixLen = 8
	APIKeySecretLen = 32
)

// ErrFeatureUnavailable may be returned (wrapped) by an APIKeyResolver when
// the org's plan does not include API access; RequireAuth answers 403
// "feature_unavailable". It wraps domain.ErrForbidden.
var ErrFeatureUnavailable = fmt.Errorf("feature unavailable: %w", domain.ErrForbidden)

// APIKeyResolver authenticates an API key (implemented by identity.Service).
// It returns domain.ErrUnauthorized / domain.ErrNotFound (wrapped) for an
// unknown, revoked or mismatching key, domain.ErrForbidden when the org may
// not use the key (e.g. closed), ErrFeatureUnavailable when the plan lacks
// API access.
type APIKeyResolver interface {
	Resolve(ctx context.Context, prefix, secret string) (*domain.APIKey, *domain.Organization, error)
}

// APIKeyResolverFunc adapts a function to APIKeyResolver.
type APIKeyResolverFunc func(ctx context.Context, prefix, secret string) (*domain.APIKey, *domain.Organization, error)

// Resolve calls f.
func (f APIKeyResolverFunc) Resolve(ctx context.Context, prefix, secret string) (*domain.APIKey, *domain.Organization, error) {
	return f(ctx, prefix, secret)
}

// Option configures RequireAuth.
type Option func(*options)

type options struct {
	apiKeys APIKeyResolver
}

// WithAPIKeys makes RequireAuth accept API keys resolved by r. A nil r
// leaves API keys disabled (they are rejected with 401).
func WithAPIKeys(r APIKeyResolver) Option {
	return func(o *options) { o.apiKeys = r }
}

// FormatAPIKey builds the plaintext key.
func FormatAPIKey(prefix, secret string) string {
	return APIKeyPrefix + prefix + "_" + secret
}

// ParseAPIKey splits a plaintext key into prefix and secret.
func ParseAPIKey(key string) (prefix, secret string, ok bool) {
	rest, found := strings.CutPrefix(key, APIKeyPrefix)
	if !found {
		return "", "", false
	}
	prefix, secret, found = strings.Cut(rest, "_")
	if !found || len(prefix) != APIKeyPrefixLen || len(secret) != APIKeySecretLen {
		return "", "", false
	}
	for _, r := range prefix {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "", "", false
		}
	}
	for _, r := range secret {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", "", false
		}
	}
	return prefix, secret, true
}

// apiKeyFromHeader returns the bearer credential when it looks like an API key.
func apiKeyFromHeader(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, strings.HasPrefix(tok, APIKeyPrefix)
}

// resolveAPIKey authenticates key; status 0 means success.
func (o options) resolveAPIKey(r *http.Request, key string) (c Claims, status int, code, msg string) {
	if o.apiKeys == nil {
		return Claims{}, http.StatusUnauthorized, "unauthorized", "API keys are not accepted here"
	}
	prefix, secret, ok := ParseAPIKey(key)
	if !ok {
		return Claims{}, http.StatusUnauthorized, "unauthorized", "invalid API key"
	}
	k, org, err := o.apiKeys.Resolve(r.Context(), prefix, secret)
	switch {
	case err == nil && k != nil:
	case err == nil, errors.Is(err, domain.ErrUnauthorized), errors.Is(err, domain.ErrNotFound):
		return Claims{}, http.StatusUnauthorized, "unauthorized", "invalid API key"
	case errors.Is(err, ErrFeatureUnavailable):
		return Claims{}, http.StatusForbidden, "feature_unavailable", "your plan does not include API access"
	case errors.Is(err, domain.ErrForbidden):
		return Claims{}, http.StatusForbidden, "forbidden", "API key is not allowed for this organisation"
	default:
		return Claims{}, http.StatusInternalServerError, "internal", "could not verify API key"
	}
	orgID := k.OrgID
	if org != nil && org.ID != uuid.Nil {
		orgID = org.ID
	}
	id := k.ID
	scopes := append([]string(nil), k.Scopes...)
	return Claims{UserID: uuid.Nil, OrgID: orgID, Role: domain.RoleAdmin, APIKeyID: &id, Scopes: scopes}, 0, "", ""
}

// RequireScope allows users (JWT) unconditionally and API keys holding
// scope (or "*"; "<res>:write" implies "<res>:read"). Run after RequireAuth.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := FromContext(r.Context())
			if !ok {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
				return
			}
			if !c.HasScope(scope) {
				WriteError(w, http.StatusForbidden, "forbidden", "API key lacks scope "+scope)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireUser rejects API-key requests (403): for account and team
// management that must be done by a person. Run after RequireAuth.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := FromContext(r.Context())
		if !ok {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
			return
		}
		if c.IsAPIKey() {
			WriteError(w, http.StatusForbidden, "forbidden", "this endpoint is not available to API keys")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PlatformAdminCheck reports whether userID is CallGo platform staff.
type PlatformAdminCheck func(ctx context.Context, userID uuid.UUID) (bool, error)

// RequirePlatformAdmin allows only platform staff (user.isPlatformAdmin) to
// continue. With a nil check it trusts the token's PlatformAdmin claim;
// otherwise check is consulted on every request (so revocation is
// immediate). API keys are always rejected. Run after RequireAuth.
func RequirePlatformAdmin(check PlatformAdminCheck) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := FromContext(r.Context())
			if !ok {
				WriteError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
				return
			}
			allowed := c.PlatformAdmin && !c.IsAPIKey()
			if check != nil && !c.IsAPIKey() {
				ok, err := check(r.Context(), c.UserID)
				if err != nil && !errors.Is(err, domain.ErrNotFound) {
					WriteError(w, http.StatusInternalServerError, "internal", "could not verify platform access")
					return
				}
				allowed = err == nil && ok
			}
			if !allowed {
				WriteError(w, http.StatusForbidden, "forbidden", "platform admin only")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
