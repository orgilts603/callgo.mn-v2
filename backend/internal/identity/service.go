// Package identity implements self-serve signup, login with rotating refresh
// tokens, email verification, password reset, team invitations and member
// management, org API keys and the audit log (docs/API.md "Identity").
//
// It depends only on domain ports: IdentityRepository and OrgRepository for
// persistence, Mailer for email, and a SubscriptionStarter (billing) to open
// the 14-day trial on signup.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/mailer"
)

// Defaults for Config durations.
const (
	DefaultInviteTTL = 7 * 24 * time.Hour
	DefaultResetTTL  = time.Hour
	DefaultVerifyTTL = 24 * time.Hour
	DefaultTimezone  = "Asia/Ulaanbaatar"
	TrialPlanCode    = "trial"
)

// SubscriptionStarter opens the trial subscription of a new org
// (implemented by internal/billing; the integrator adapts it).
type SubscriptionStarter interface {
	StartTrial(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error)
}

// UserDeleter is an optional extension of the repositories (IdentityRepository
// or OrgRepository) used by RemoveMember. Without it removed members are
// disabled instead of deleted.
type UserDeleter interface {
	DeleteUser(ctx context.Context, id uuid.UUID) error
}

// Config holds identity settings.
type Config struct {
	// AppURL is the public dashboard origin used in email links
	// (CALLGO_APP_URL, e.g. https://app.callgo.mn).
	AppURL     string
	AccessTTL  time.Duration // default auth.DefaultTokenTTL (15 min)
	RefreshTTL time.Duration // default auth.DefaultRefreshTTL (30 days)
	InviteTTL  time.Duration // default 7 days
	ResetTTL   time.Duration // default 1 hour
	VerifyTTL  time.Duration // default 24 hours
	// AllowSignup enables POST /api/auth/signup (CALLGO_ALLOW_SIGNUP).
	AllowSignup bool
	// JWTSecret signs access tokens and keys refresh-token MACs.
	JWTSecret []byte
}

// Client describes the caller of a session-creating request.
type Client struct {
	IP        string
	UserAgent string
}

// Actor is who performs a mutation (from the request's auth claims).
type Actor struct {
	UserID   uuid.UUID
	OrgID    uuid.UUID
	Role     domain.Role
	Email    string
	APIKeyID *uuid.UUID
}

// ActorFromClaims converts auth claims to an Actor.
func ActorFromClaims(c auth.Claims) Actor {
	return Actor{UserID: c.UserID, OrgID: c.OrgID, Role: c.Role, APIKeyID: c.APIKeyID}
}

func (a Actor) isAdmin() bool { return a.Role == domain.RoleOwner || a.Role == domain.RoleAdmin }

// AuthResult is returned by signup, login, refresh and invitation acceptance.
// Its JSON is the API's auth response.
type AuthResult struct {
	AccessToken  string               `json:"token"`
	RefreshToken string               `json:"refreshToken"`
	User         *domain.User         `json:"user"`
	Org          *domain.Organization `json:"org"`
	Subscription *domain.Subscription `json:"subscription"`
}

// Service implements the identity use cases.
type Service struct {
	repo  domain.IdentityRepository
	users domain.OrgRepository
	mail  domain.Mailer
	subs  SubscriptionStarter
	cfg   Config
	log   zerolog.Logger
	tpl   mailer.Templates

	// Limits returns the org's effective plan limits (quota for invitations,
	// "plan" in GET /api/org). Nil disables the user quota.
	Limits func(ctx context.Context, orgID uuid.UUID) (domain.Plan, error)
	// Subscription returns the org's current subscription for auth
	// responses and GET /api/org. Nil (or ErrNotFound) yields null.
	Subscription func(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error)
	// HasFeature, when set, makes ResolveAPIKey reject keys of orgs whose
	// plan lacks the "api" feature (auth.ErrFeatureUnavailable).
	HasFeature func(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)

	now        func() time.Time
	refreshKey []byte

	dummyOnce sync.Once
	dummyHash string
}

// New builds the service. mail may be nil (emails are then only logged);
// subs may be nil (no trial is started, e.g. in tests).
func New(repo domain.IdentityRepository, users domain.OrgRepository, mail domain.Mailer, subs SubscriptionStarter, cfg Config, log zerolog.Logger) *Service {
	log = log.With().Str("component", "identity").Logger()
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = auth.DefaultTokenTTL
	}
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = auth.DefaultRefreshTTL
	}
	if cfg.InviteTTL <= 0 {
		cfg.InviteTTL = DefaultInviteTTL
	}
	if cfg.ResetTTL <= 0 {
		cfg.ResetTTL = DefaultResetTTL
	}
	if cfg.VerifyTTL <= 0 {
		cfg.VerifyTTL = DefaultVerifyTTL
	}
	if mail == nil {
		mail = mailer.NewLog(log)
	}
	k := sha256.Sum256(append([]byte("callgo-refresh-token-v1:"), cfg.JWTSecret...))
	return &Service{
		repo: repo, users: users, mail: mail, subs: subs, cfg: cfg, log: log,
		tpl:        mailer.NewTemplates(cfg.AppURL),
		now:        func() time.Time { return time.Now().UTC() },
		refreshKey: k[:],
	}
}

// Config returns the effective configuration (defaults applied).
func (s *Service) Config() Config { return s.cfg }

// ---- validation helpers ----

// NormalizeEmail lowercases and trims an address.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func validEmail(e string) bool {
	if e == "" || len(e) > 254 {
		return false
	}
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e && strings.Contains(e[strings.LastIndexByte(e, '@')+1:], ".")
}

func validatePassword(p string) error {
	if utf8.RuneCountInString(p) < 8 {
		return errInvalid("password must be at least 8 characters")
	}
	if len(p) > 72 {
		return errInvalid("password must be at most 72 bytes")
	}
	return nil
}

func validateName(field, v string, maxLen int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errInvalid("%s is required", field)
	}
	if utf8.RuneCountInString(v) > maxLen {
		return "", errInvalid("%s must be at most %d characters", field, maxLen)
	}
	return v, nil
}

func validRole(r domain.Role) bool {
	return r == domain.RoleOwner || r == domain.RoleAdmin || r == domain.RoleOperator
}

// userActive treats the legacy empty status as active.
func userActive(u *domain.User) bool { return u.Status == domain.UserActive || u.Status == "" }

// ---- tokens ----

// newToken returns a random URL-safe token (256 bits) and its sha256 hash.
func newToken() (plain, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("random token: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(b[:])
	return plain, HashToken(plain), nil
}

// HashToken is the sha256 hex digest stored for every secret token.
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

const (
	refreshRandLen = 32
	refreshMACLen  = 16
	refreshRawLen  = 16 + 16 + refreshRandLen + refreshMACLen
)

// newRefreshToken encodes userID|sessionID|random|mac. The MAC lets Refresh
// recognise a genuine but already-rotated token (reuse) without storing old
// hashes, while forged tokens are simply rejected.
func (s *Service) newRefreshToken(userID, sessionID uuid.UUID) (string, error) {
	raw := make([]byte, 0, refreshRawLen)
	raw = append(raw, userID[:]...)
	raw = append(raw, sessionID[:]...)
	rnd := make([]byte, refreshRandLen)
	if _, err := rand.Read(rnd); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	raw = append(raw, rnd...)
	m := hmac.New(sha256.New, s.refreshKey)
	m.Write(raw)
	raw = append(raw, m.Sum(nil)[:refreshMACLen]...)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Service) parseRefreshToken(tok string) (userID, sessionID uuid.UUID, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != refreshRawLen {
		return uuid.Nil, uuid.Nil, false
	}
	body, mac := raw[:refreshRawLen-refreshMACLen], raw[refreshRawLen-refreshMACLen:]
	m := hmac.New(sha256.New, s.refreshKey)
	m.Write(body)
	if !hmac.Equal(mac, m.Sum(nil)[:refreshMACLen]) {
		return uuid.Nil, uuid.Nil, false
	}
	copy(userID[:], body[:16])
	copy(sessionID[:], body[16:32])
	return userID, sessionID, true
}

// issue creates a refresh session and an access token for u.
func (s *Service) issue(ctx context.Context, u *domain.User, org *domain.Organization, c Client) (*AuthResult, error) {
	now := s.now()
	sess := &domain.RefreshSession{
		ID: uuid.New(), UserID: u.ID, OrgID: u.OrgID,
		UserAgent: truncate(c.UserAgent, 512), IP: c.IP,
		ExpiresAt: now.Add(s.cfg.RefreshTTL), LastUsed: now, CreatedAt: now,
	}
	rt, err := s.newRefreshToken(u.ID, sess.ID)
	if err != nil {
		return nil, err
	}
	sess.TokenHash = HashToken(rt)
	if err := s.repo.CreateRefreshSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("create refresh session: %w", err)
	}
	at, err := s.accessToken(u)
	if err != nil {
		return nil, err
	}
	return &AuthResult{AccessToken: at, RefreshToken: rt, User: u, Org: org, Subscription: s.subscription(ctx, org.ID)}, nil
}

func (s *Service) accessToken(u *domain.User) (string, error) {
	tok, err := auth.IssueToken(s.cfg.JWTSecret, auth.Claims{UserID: u.ID, OrgID: u.OrgID, Role: u.Role, PlatformAdmin: u.IsPlatformAdmin}, s.cfg.AccessTTL)
	if err != nil {
		return "", fmt.Errorf("issue access token: %w", err)
	}
	return tok, nil
}

func (s *Service) subscription(ctx context.Context, orgID uuid.UUID) *domain.Subscription {
	if s.Subscription == nil {
		return nil
	}
	sub, err := s.Subscription(ctx, orgID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.log.Warn().Err(err).Stringer("orgId", orgID).Msg("load subscription")
		}
		return nil
	}
	return sub
}

// dummyPasswordHash equalises login timing for unknown emails.
func (s *Service) dummyPasswordHash() string {
	s.dummyOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte(uuid.NewString()), bcrypt.DefaultCost)
		if err == nil {
			s.dummyHash = string(h)
		}
	})
	return s.dummyHash
}

// sendMail renders nothing itself; it delivers msg and logs failures (email
// problems never fail the user-facing request).
func (s *Service) sendMail(ctx context.Context, to string, msg mailer.Message, err error) {
	if err != nil {
		s.log.Error().Err(err).Msg("render email")
		return
	}
	if err := s.mail.Send(ctx, to, msg.Subject, msg.Text, msg.HTML); err != nil {
		s.log.Error().Err(err).Str("to", to).Str("subject", msg.Subject).Msg("send email")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (s *Service) getOrg(ctx context.Context, id uuid.UUID) (*domain.Organization, error) {
	o, err := s.users.GetOrg(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, errNotFound("organisation")
	}
	if err != nil {
		return nil, fmt.Errorf("get org: %w", err)
	}
	return o, nil
}

// memberOf loads a user and checks it belongs to orgID.
func (s *Service) memberOf(ctx context.Context, orgID, userID uuid.UUID) (*domain.User, error) {
	u, err := s.users.GetUser(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && u.OrgID != orgID) {
		return nil, errNotFound("user")
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}
