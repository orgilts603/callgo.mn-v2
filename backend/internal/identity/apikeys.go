package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// FeatureAPI is the plan feature that enables API keys.
const FeatureAPI = "api"

// touchInterval throttles LastUsedAt writes for busy keys.
const touchInterval = time.Minute

var scopeRe = regexp.MustCompile(`^(\*|[a-z][a-z_]{0,39}:(read|write))$`)

const (
	lowerAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"
	mixedAlnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

func randomString(alphabet string, n int) (string, error) {
	b := make([]byte, n)
	limit := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("random: %w", err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b), nil
}

// NormalizeScopes validates and de-duplicates API key scopes
// ("<resource>:read|write" or "*").
func NormalizeScopes(scopes []string) ([]string, error) {
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		sc = strings.TrimSpace(sc)
		if !scopeRe.MatchString(sc) {
			return nil, errInvalid("invalid scope %q (use \"<resource>:read\", \"<resource>:write\" or \"*\")", sc)
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, errInvalid("at least one scope is required")
	}
	return out, nil
}

// CreateAPIKey issues a key for the actor's org. The plaintext
// ("cg_live_<prefix>_<secret>") is returned once; only its sha256 is stored.
func (s *Service) CreateAPIKey(ctx context.Context, actor Actor, name string, scopes []string) (*domain.APIKey, string, error) {
	if !actor.isAdmin() || actor.APIKeyID != nil {
		return nil, "", errForbidden("only owners and admins can create API keys")
	}
	name, err := validateName("name", name, 100)
	if err != nil {
		return nil, "", err
	}
	scopes, err = NormalizeScopes(scopes)
	if err != nil {
		return nil, "", err
	}
	for range 5 {
		prefix, err := randomString(lowerAlnum, auth.APIKeyPrefixLen)
		if err != nil {
			return nil, "", err
		}
		secret, err := randomString(mixedAlnum, auth.APIKeySecretLen)
		if err != nil {
			return nil, "", err
		}
		if _, err := s.repo.GetAPIKeyByPrefix(ctx, prefix); err == nil {
			continue // prefix collision
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, "", fmt.Errorf("lookup api key prefix: %w", err)
		}
		plain := auth.FormatAPIKey(prefix, secret)
		k := &domain.APIKey{
			ID: uuid.New(), OrgID: actor.OrgID, Name: name, Prefix: prefix, KeyHash: HashToken(plain),
			Scopes: scopes, CreatedAt: s.now(),
		}
		if actor.UserID != uuid.Nil {
			uid := actor.UserID
			k.CreatedBy = &uid
		}
		if err := s.repo.CreateAPIKey(ctx, k); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			return nil, "", fmt.Errorf("create api key: %w", err)
		}
		return k, plain, nil
	}
	return nil, "", errors.New("create api key: could not allocate a unique prefix")
}

// ListAPIKeys lists the org's keys (revoked ones included).
func (s *Service) ListAPIKeys(ctx context.Context, orgID uuid.UUID) ([]domain.APIKey, error) {
	keys, err := s.repo.ListAPIKeys(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

// RevokeAPIKey revokes one of the org's keys.
func (s *Service) RevokeAPIKey(ctx context.Context, orgID, keyID uuid.UUID) (*domain.APIKey, error) {
	keys, err := s.ListAPIKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.ID != keyID {
			continue
		}
		if k.RevokedAt == nil {
			if err := s.repo.RevokeAPIKey(ctx, k.ID); err != nil {
				return nil, fmt.Errorf("revoke api key: %w", err)
			}
		}
		return &k, nil
	}
	return nil, errNotFound("API key")
}

// ResolveAPIKey authenticates a presented key: unknown, revoked or
// mismatching keys → ErrUnauthorized; closed org → ErrForbidden; plan without
// the "api" feature (when HasFeature is set) → auth.ErrFeatureUnavailable.
func (s *Service) ResolveAPIKey(ctx context.Context, prefix, secret string) (*domain.APIKey, *domain.Organization, error) {
	invalid := errUnauthorized("invalid API key")
	k, err := s.repo.GetAPIKeyByPrefix(ctx, prefix)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil, invalid
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get api key: %w", err)
	}
	want := HashToken(auth.FormatAPIKey(prefix, secret))
	if subtle.ConstantTimeCompare([]byte(want), []byte(k.KeyHash)) != 1 || k.RevokedAt != nil {
		return nil, nil, invalid
	}
	org, err := s.getOrg(ctx, k.OrgID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil, invalid
		}
		return nil, nil, err
	}
	if org.Status == domain.OrgClosed {
		return nil, nil, errForbidden("organisation is closed")
	}
	if s.HasFeature != nil {
		ok, err := s.HasFeature(ctx, org.ID, FeatureAPI)
		if err != nil {
			return nil, nil, fmt.Errorf("check api feature: %w", err)
		}
		if !ok {
			return nil, nil, auth.ErrFeatureUnavailable
		}
	}
	if k.LastUsedAt == nil || s.now().Sub(*k.LastUsedAt) >= touchInterval {
		if err := s.repo.TouchAPIKey(ctx, k.ID); err != nil {
			s.log.Warn().Err(err).Stringer("apiKeyId", k.ID).Msg("touch api key")
		}
	}
	return k, org, nil
}

// Resolve implements auth.APIKeyResolver.
func (s *Service) Resolve(ctx context.Context, prefix, secret string) (*domain.APIKey, *domain.Organization, error) {
	return s.ResolveAPIKey(ctx, prefix, secret)
}

var _ auth.APIKeyResolver = (*Service)(nil)
