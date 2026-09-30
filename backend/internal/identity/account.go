package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// SignupInput is the POST /api/auth/signup body.
type SignupInput struct {
	OrgName  string `json:"orgName"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Phone    string `json:"phone,omitempty"`
}

// Signup creates an organisation (status active, plan trial), its owner
// (status active, email unverified), starts the trial subscription, sends
// the verification email and opens a session.
func (s *Service) Signup(ctx context.Context, in SignupInput, c Client) (*AuthResult, error) {
	if !s.cfg.AllowSignup {
		return nil, errForbidden("signup is disabled")
	}
	orgName, err := validateName("orgName", in.OrgName, 120)
	if err != nil {
		return nil, err
	}
	name, err := validateName("name", in.Name, 120)
	if err != nil {
		return nil, err
	}
	email := NormalizeEmail(in.Email)
	if !validEmail(email) {
		return nil, errInvalid("email is invalid")
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	phone := strings.TrimSpace(in.Phone)
	if len(phone) > 32 {
		return nil, errInvalid("phone is too long")
	}
	if _, err := s.users.GetUserByEmail(ctx, email); err == nil {
		return nil, errConflict("email already registered")
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	now := s.now()
	org := &domain.Organization{
		ID: uuid.New(), Name: orgName, PlanCode: TrialPlanCode, Status: domain.OrgActive,
		Timezone: DefaultTimezone, Settings: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	if phone != "" {
		org.Settings["contactPhone"] = phone
	}
	if err := s.createOrgWithUniqueSlug(ctx, org); err != nil {
		return nil, err
	}
	u := &domain.User{
		ID: uuid.New(), OrgID: org.ID, Email: email, Name: name, Role: domain.RoleOwner,
		PasswordHash: hash, Status: domain.UserActive, LastLoginAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.users.CreateUser(ctx, u); err != nil {
		// No transaction spans both repositories: close the orphan org.
		org.Status = domain.OrgClosed
		if cerr := s.repo.UpdateOrg(ctx, org); cerr != nil {
			s.log.Error().Err(cerr).Stringer("orgId", org.ID).Msg("close orphan org after failed signup")
		}
		if errors.Is(err, domain.ErrConflict) {
			return nil, errConflict("email already registered")
		}
		return nil, fmt.Errorf("create owner: %w", err)
	}
	if s.subs != nil {
		if _, err := s.subs.StartTrial(ctx, org.ID); err != nil {
			// The account exists; billing can open the subscription lazily.
			s.log.Error().Err(err).Stringer("orgId", org.ID).Msg("start trial subscription")
		}
	}
	s.sendVerification(ctx, u)
	res, err := s.issue(ctx, u, org, c)
	if err != nil {
		return nil, err
	}
	_ = s.Audit(ctx, org.ID, Actor{UserID: u.ID, OrgID: org.ID, Role: u.Role, Email: u.Email}, "org.signup", "organization", org.ID.String(),
		map[string]any{"slug": org.Slug}, c.IP)
	return res, nil
}

// createOrgWithUniqueSlug assigns slug, slug-2, slug-3, … and falls back to
// a random suffix; a conflict on insert (race) retries.
func (s *Service) createOrgWithUniqueSlug(ctx context.Context, org *domain.Organization) error {
	base := Slugify(org.Name)
	if base == "" {
		base = "org"
	}
	candidates := make([]string, 0, 12)
	candidates = append(candidates, base)
	for i := 2; i <= 10; i++ {
		candidates = append(candidates, base+"-"+strconv.Itoa(i))
	}
	for range 3 {
		var b [3]byte
		_, _ = rand.Read(b[:])
		candidates = append(candidates, base+"-"+hex.EncodeToString(b[:]))
	}
	for _, slug := range candidates {
		_, err := s.users.GetOrgBySlug(ctx, slug)
		if err == nil {
			continue
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("lookup slug: %w", err)
		}
		org.Slug = slug
		err = s.repo.CreateOrg(ctx, org)
		if err == nil {
			return nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			return fmt.Errorf("create org: %w", err)
		}
	}
	return errConflict("could not allocate an organisation slug")
}

// Login authenticates by email and password. Disabled / not-yet-activated
// users and closed orgs are rejected; suspended orgs may log in (to pay).
func (s *Service) Login(ctx context.Context, email, password string, c Client) (*AuthResult, error) {
	email = NormalizeEmail(email)
	if email == "" || password == "" {
		return nil, errInvalid("email and password are required")
	}
	u, err := s.users.GetUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if u == nil || err != nil {
		auth.CheckPassword(s.dummyPasswordHash(), password)
		return nil, errUnauthorized("invalid email or password")
	}
	if !auth.CheckPassword(u.PasswordHash, password) {
		return nil, errUnauthorized("invalid email or password")
	}
	switch {
	case u.Status == domain.UserDisabled:
		return nil, errForbidden("account is disabled")
	case u.Status == domain.UserInvited:
		return nil, errForbidden("account is not activated; accept the invitation first")
	}
	org, err := s.getOrg(ctx, u.OrgID)
	if err != nil {
		return nil, err
	}
	if org.Status == domain.OrgClosed {
		return nil, errForbidden("organisation is closed")
	}
	now := s.now()
	u.LastLoginAt = &now
	if err := s.repo.UpdateUser(ctx, u); err != nil {
		s.log.Warn().Err(err).Stringer("userId", u.ID).Msg("update last login")
	}
	res, err := s.issue(ctx, u, org, c)
	if err != nil {
		return nil, err
	}
	_ = s.Audit(ctx, org.ID, Actor{UserID: u.ID, OrgID: org.ID, Role: u.Role, Email: u.Email}, "auth.login", "user", u.ID.String(), nil, c.IP)
	return res, nil
}

// Refresh rotates a refresh token. Presenting an already-rotated token
// (reuse — the token was probably stolen) revokes every session of the user.
func (s *Service) Refresh(ctx context.Context, refreshToken string, c Client) (*AuthResult, error) {
	invalid := errUnauthorized("invalid or expired refresh token")
	userID, sessionID, ok := s.parseRefreshToken(strings.TrimSpace(refreshToken))
	if !ok {
		return nil, invalid
	}
	sess, err := s.repo.GetRefreshSessionByHash(ctx, HashToken(refreshToken))
	if errors.Is(err, domain.ErrNotFound) {
		s.detectReuse(ctx, userID, sessionID, c)
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get refresh session: %w", err)
	}
	now := s.now()
	if sess.ID != sessionID || sess.UserID != userID || sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
		return nil, invalid
	}
	u, err := s.users.GetUser(ctx, sess.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if !userActive(u) {
		_ = s.repo.RevokeUserSessions(ctx, u.ID)
		return nil, errUnauthorized("account is disabled")
	}
	org, err := s.getOrg(ctx, u.OrgID)
	if err != nil {
		return nil, err
	}
	if org.Status == domain.OrgClosed {
		return nil, errForbidden("organisation is closed")
	}
	rt, err := s.newRefreshToken(u.ID, sess.ID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.RotateRefreshSession(ctx, sess.ID, HashToken(rt), now.Add(s.cfg.RefreshTTL)); err != nil {
		return nil, fmt.Errorf("rotate refresh session: %w", err)
	}
	at, err := s.accessToken(u)
	if err != nil {
		return nil, err
	}
	return &AuthResult{AccessToken: at, RefreshToken: rt, User: u, Org: org, Subscription: s.subscription(ctx, org.ID)}, nil
}

// detectReuse revokes all sessions of userID when sessionID is a live
// session: the presented token was authentic (valid MAC) but already rotated.
func (s *Service) detectReuse(ctx context.Context, userID, sessionID uuid.UUID, c Client) {
	sessions, err := s.repo.ListRefreshSessions(ctx, userID)
	if err != nil {
		s.log.Error().Err(err).Msg("list sessions for reuse detection")
		return
	}
	for _, sess := range sessions {
		if sess.ID != sessionID || sess.RevokedAt != nil {
			continue
		}
		if err := s.repo.RevokeUserSessions(ctx, userID); err != nil {
			s.log.Error().Err(err).Stringer("userId", userID).Msg("revoke sessions after refresh-token reuse")
		}
		s.log.Warn().Stringer("userId", userID).Stringer("sessionId", sessionID).Str("ip", c.IP).
			Msg("refresh token reuse detected: all sessions revoked")
		_ = s.Audit(ctx, sess.OrgID, Actor{UserID: userID, OrgID: sess.OrgID}, "session.reuse_detected", "session", sessionID.String(),
			map[string]any{"userAgent": c.UserAgent}, c.IP)
		return
	}
}

// Logout revokes the session of refreshToken. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil
	}
	sess, err := s.repo.GetRefreshSessionByHash(ctx, HashToken(refreshToken))
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get refresh session: %w", err)
	}
	if sess.RevokedAt != nil {
		return nil
	}
	if err := s.repo.RevokeRefreshSession(ctx, sess.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// sendVerification creates a verification token and emails the link.
func (s *Service) sendVerification(ctx context.Context, u *domain.User) {
	plain, hash, err := newToken()
	if err != nil {
		s.log.Error().Err(err).Msg("verification token")
		return
	}
	v := &domain.EmailVerification{ID: uuid.New(), UserID: u.ID, TokenHash: hash, ExpiresAt: s.now().Add(s.cfg.VerifyTTL)}
	if err := s.repo.CreateEmailVerification(ctx, v); err != nil {
		s.log.Error().Err(err).Stringer("userId", u.ID).Msg("create email verification")
		return
	}
	msg, err := s.tpl.Verification(u.Name, plain, s.cfg.VerifyTTL)
	s.sendMail(ctx, u.Email, msg, err)
}

// VerifyEmail consumes a verification token and marks the email verified.
func (s *Service) VerifyEmail(ctx context.Context, token string) (*domain.User, error) {
	invalid := errInvalid("verification link is invalid or has expired")
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, invalid
	}
	v, err := s.repo.GetEmailVerificationByHash(ctx, HashToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get verification: %w", err)
	}
	if v.UsedAt != nil || !s.now().Before(v.ExpiresAt) {
		return nil, invalid
	}
	u, err := s.users.GetUser(ctx, v.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if err := s.repo.MarkEmailVerificationUsed(ctx, v.ID); err != nil {
		return nil, fmt.Errorf("mark verification used: %w", err)
	}
	if u.EmailVerifiedAt == nil {
		now := s.now()
		u.EmailVerifiedAt = &now
		if err := s.repo.UpdateUser(ctx, u); err != nil {
			return nil, fmt.Errorf("update user: %w", err)
		}
	}
	return u, nil
}

// ResendVerification emails a new verification link to an existing,
// unverified, active user. It never reveals whether the email exists.
func (s *Service) ResendVerification(ctx context.Context, email string) error {
	u, err := s.users.GetUserByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.EmailVerifiedAt != nil || !userActive(u) {
		return nil
	}
	s.sendVerification(ctx, u)
	return nil
}

// ResendVerificationFor emails a new verification link to the signed-in
// user unless already verified.
func (s *Service) ResendVerificationFor(ctx context.Context, userID uuid.UUID) error {
	u, err := s.users.GetUser(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return errNotFound("user")
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.EmailVerifiedAt != nil || !userActive(u) {
		return nil
	}
	s.sendVerification(ctx, u)
	return nil
}

// ForgotPassword emails a single-use reset link (valid ResetTTL) when the
// address belongs to an active user. It always succeeds from the caller's
// point of view (no account enumeration); failures are logged.
func (s *Service) ForgotPassword(ctx context.Context, email string) {
	email = NormalizeEmail(email)
	if !validEmail(email) {
		return
	}
	u, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			s.log.Error().Err(err).Msg("forgot password: lookup user")
		}
		return
	}
	if !userActive(u) {
		return
	}
	plain, hash, err := newToken()
	if err != nil {
		s.log.Error().Err(err).Msg("reset token")
		return
	}
	now := s.now()
	r := &domain.PasswordReset{ID: uuid.New(), UserID: u.ID, TokenHash: hash, ExpiresAt: now.Add(s.cfg.ResetTTL), CreatedAt: now}
	if err := s.repo.CreatePasswordReset(ctx, r); err != nil {
		s.log.Error().Err(err).Stringer("userId", u.ID).Msg("create password reset")
		return
	}
	msg, err := s.tpl.PasswordReset(u.Name, plain, s.cfg.ResetTTL)
	s.sendMail(ctx, u.Email, msg, err)
}

// ResetPassword sets a new password from a reset token and revokes every
// session of the user. It returns the user (for auditing).
func (s *Service) ResetPassword(ctx context.Context, token, password string) (*domain.User, error) {
	invalid := errInvalid("reset link is invalid or has expired")
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, invalid
	}
	r, err := s.repo.GetPasswordResetByHash(ctx, HashToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get password reset: %w", err)
	}
	if r.UsedAt != nil || !s.now().Before(r.ExpiresAt) {
		return nil, invalid
	}
	u, err := s.users.GetUser(ctx, r.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, invalid
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if !userActive(u) {
		return nil, errForbidden("account is disabled")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	// Single use first: a failure after this point leaves the token spent.
	if err := s.repo.MarkPasswordResetUsed(ctx, r.ID); err != nil {
		return nil, fmt.Errorf("mark reset used: %w", err)
	}
	u.PasswordHash = hash
	if u.EmailVerifiedAt == nil {
		// The link proved control of the mailbox.
		now := s.now()
		u.EmailVerifiedAt = &now
	}
	if err := s.repo.UpdateUser(ctx, u); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	if err := s.repo.RevokeUserSessions(ctx, u.ID); err != nil {
		return nil, fmt.Errorf("revoke sessions: %w", err)
	}
	return u, nil
}

// ChangePassword verifies the current password and sets a new one. Every
// other session is revoked; the session of keepRefreshToken (if given)
// survives so the caller stays logged in.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, next, keepRefreshToken string) error {
	if err := validatePassword(next); err != nil {
		return err
	}
	u, err := s.users.GetUser(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return errNotFound("user")
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if !auth.CheckPassword(u.PasswordHash, current) {
		// Not 401: the client's token is fine, the input is wrong.
		return errInvalid("current password is incorrect")
	}
	if current == next {
		return errInvalid("new password must differ from the current one")
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	if err := s.repo.UpdateUser(ctx, u); err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	keepHash := ""
	if keepRefreshToken != "" {
		keepHash = HashToken(strings.TrimSpace(keepRefreshToken))
	}
	sessions, err := s.repo.ListRefreshSessions(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	for _, sess := range sessions {
		if sess.RevokedAt != nil || (keepHash != "" && sess.TokenHash == keepHash) {
			continue
		}
		if err := s.repo.RevokeRefreshSession(ctx, sess.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("revoke session: %w", err)
		}
	}
	return nil
}

// ListSessions returns the user's live (not revoked, not expired) sessions.
func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID) ([]domain.RefreshSession, error) {
	all, err := s.repo.ListRefreshSessions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	now := s.now()
	out := make([]domain.RefreshSession, 0, len(all))
	for _, sess := range all {
		if sess.RevokedAt == nil && now.Before(sess.ExpiresAt) {
			out = append(out, sess)
		}
	}
	return out, nil
}

// RevokeSession revokes one of the user's own sessions.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID uuid.UUID) error {
	all, err := s.repo.ListRefreshSessions(ctx, userID)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	for _, sess := range all {
		if sess.ID != sessionID {
			continue
		}
		if sess.RevokedAt != nil {
			return nil
		}
		if err := s.repo.RevokeRefreshSession(ctx, sess.ID); err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
		return nil
	}
	return errNotFound("session")
}

// expired reports whether t is not after now.
func expired(now, t time.Time) bool { return !now.Before(t) }
