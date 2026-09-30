package crm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Compile-time proof that *Store satisfies the identity port.
var _ domain.IdentityRepository = (*Store)(nil)

// Token hashes (invitations, password resets, e-mail verifications, refresh
// sessions, API keys) are opaque strings computed by the caller; this package
// never sees plaintext tokens. Lookups by hash return domain.ErrNotFound when
// nothing matches and return used / expired / revoked rows as they are: the
// caller decides validity. One-time tokens are consumed with the Mark*
// methods, which fail with domain.ErrConflict when already consumed so two
// concurrent redemptions cannot both succeed.

// consume runs an UPDATE … WHERE id = $1 AND <still unused> statement and maps
// "no row updated" to ErrNotFound (no such id) or ErrConflict (already used).
func (s *Store) consume(ctx context.Context, op, table, update string, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, update, id)
	if err != nil {
		return dbErr(op, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&exists); err != nil {
		return dbErr(op, err)
	}
	if !exists {
		return fmt.Errorf("crm: %s: %w", op, domain.ErrNotFound)
	}
	return fmt.Errorf("crm: %s: %w", op, domain.ErrConflict)
}

// ---------------------------------------------------------------------------
// Invitations
// ---------------------------------------------------------------------------

const invitationCols = `id, org_id, email, role, token_hash, invited_by, expires_at, accepted_at, created_at`

func scanInvitation(row pgx.Row) (*domain.Invitation, error) {
	var inv domain.Invitation
	if err := row.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.TokenHash, &inv.InvitedBy,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
		return nil, err
	}
	return &inv, nil
}

// CreateInvitation inserts an invitation. Role defaults to operator. A second
// open (unaccepted) invitation for the same e-mail in the same org, or a
// duplicate token hash, is ErrConflict.
func (s *Store) CreateInvitation(ctx context.Context, inv *domain.Invitation) error {
	inv.Email = strings.TrimSpace(inv.Email)
	if inv.Role == "" {
		inv.Role = domain.RoleOperator
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO invitations (id, org_id, email, role, token_hash, invited_by, expires_at, created_at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, COALESCE($8, now()))
		 RETURNING id, created_at`,
		nilIfZero(inv.ID), inv.OrgID, inv.Email, string(inv.Role), inv.TokenHash, inv.InvitedBy,
		inv.ExpiresAt, nilIfZeroTime(inv.CreatedAt))
	return dbErr("create invitation", row.Scan(&inv.ID, &inv.CreatedAt))
}

// GetInvitationByHash returns the invitation with tokenHash (accepted or
// expired ones included).
func (s *Store) GetInvitationByHash(ctx context.Context, tokenHash string) (*domain.Invitation, error) {
	inv, err := scanInvitation(s.db.QueryRow(ctx,
		`SELECT `+invitationCols+` FROM invitations WHERE token_hash = $1`, tokenHash))
	return inv, dbErr("get invitation by hash", err)
}

// ListInvitations lists an organisation's open (not yet accepted)
// invitations, newest first; expired ones are included so they can be resent
// or deleted.
func (s *Store) ListInvitations(ctx context.Context, orgID uuid.UUID) ([]domain.Invitation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+invitationCols+` FROM invitations WHERE org_id = $1 AND accepted_at IS NULL
		 ORDER BY created_at DESC, id`, orgID)
	if err != nil {
		return nil, dbErr("list invitations", err)
	}
	return collect("list invitations", rows, scanInvitation)
}

// DeleteInvitation removes an invitation (revoke).
func (s *Store) DeleteInvitation(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, id)
	return affected("delete invitation", tag, err)
}

// MarkInvitationAccepted stamps accepted_at. ErrConflict when it was already
// accepted.
func (s *Store) MarkInvitationAccepted(ctx context.Context, id uuid.UUID) error {
	return s.consume(ctx, "mark invitation accepted", "invitations",
		`UPDATE invitations SET accepted_at = now() WHERE id = $1 AND accepted_at IS NULL`, id)
}

// ---------------------------------------------------------------------------
// Password resets and e-mail verifications
// ---------------------------------------------------------------------------

const passwordResetCols = `id, user_id, token_hash, expires_at, used_at, created_at`

func scanPasswordReset(row pgx.Row) (*domain.PasswordReset, error) {
	var r domain.PasswordReset
	if err := row.Scan(&r.ID, &r.UserID, &r.TokenHash, &r.ExpiresAt, &r.UsedAt, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreatePasswordReset inserts a reset token.
func (s *Store) CreatePasswordReset(ctx context.Context, r *domain.PasswordReset) error {
	row := s.db.QueryRow(ctx,
		`INSERT INTO password_resets (id, user_id, token_hash, expires_at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4)
		 RETURNING id, created_at`,
		nilIfZero(r.ID), r.UserID, r.TokenHash, r.ExpiresAt)
	return dbErr("create password reset", row.Scan(&r.ID, &r.CreatedAt))
}

// GetPasswordResetByHash returns the reset with tokenHash (used or expired
// ones included).
func (s *Store) GetPasswordResetByHash(ctx context.Context, tokenHash string) (*domain.PasswordReset, error) {
	r, err := scanPasswordReset(s.db.QueryRow(ctx,
		`SELECT `+passwordResetCols+` FROM password_resets WHERE token_hash = $1`, tokenHash))
	return r, dbErr("get password reset by hash", err)
}

// MarkPasswordResetUsed stamps used_at. ErrConflict when already used.
func (s *Store) MarkPasswordResetUsed(ctx context.Context, id uuid.UUID) error {
	return s.consume(ctx, "mark password reset used", "password_resets",
		`UPDATE password_resets SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
}

const emailVerificationCols = `id, user_id, token_hash, expires_at, used_at`

func scanEmailVerification(row pgx.Row) (*domain.EmailVerification, error) {
	var v domain.EmailVerification
	if err := row.Scan(&v.ID, &v.UserID, &v.TokenHash, &v.ExpiresAt, &v.UsedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateEmailVerification inserts a verification token.
func (s *Store) CreateEmailVerification(ctx context.Context, v *domain.EmailVerification) error {
	row := s.db.QueryRow(ctx,
		`INSERT INTO email_verifications (id, user_id, token_hash, expires_at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4)
		 RETURNING id`,
		nilIfZero(v.ID), v.UserID, v.TokenHash, v.ExpiresAt)
	return dbErr("create email verification", row.Scan(&v.ID))
}

// GetEmailVerificationByHash returns the verification with tokenHash (used
// or expired ones included).
func (s *Store) GetEmailVerificationByHash(ctx context.Context, tokenHash string) (*domain.EmailVerification, error) {
	v, err := scanEmailVerification(s.db.QueryRow(ctx,
		`SELECT `+emailVerificationCols+` FROM email_verifications WHERE token_hash = $1`, tokenHash))
	return v, dbErr("get email verification by hash", err)
}

// MarkEmailVerificationUsed stamps used_at. ErrConflict when already used.
func (s *Store) MarkEmailVerificationUsed(ctx context.Context, id uuid.UUID) error {
	return s.consume(ctx, "mark email verification used", "email_verifications",
		`UPDATE email_verifications SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
}

// ---------------------------------------------------------------------------
// Refresh sessions
// ---------------------------------------------------------------------------

const refreshSessionCols = `id, user_id, org_id, token_hash, user_agent, ip, expires_at, revoked_at,
	last_used_at, created_at`

func scanRefreshSession(row pgx.Row) (*domain.RefreshSession, error) {
	var rs domain.RefreshSession
	if err := row.Scan(&rs.ID, &rs.UserID, &rs.OrgID, &rs.TokenHash, &rs.UserAgent, &rs.IP, &rs.ExpiresAt,
		&rs.RevokedAt, &rs.LastUsed, &rs.CreatedAt); err != nil {
		return nil, err
	}
	return &rs, nil
}

// CreateRefreshSession inserts a login session. LastUsed defaults to now.
func (s *Store) CreateRefreshSession(ctx context.Context, rs *domain.RefreshSession) error {
	row := s.db.QueryRow(ctx,
		`INSERT INTO refresh_sessions (id, user_id, org_id, token_hash, user_agent, ip, expires_at, last_used_at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, COALESCE($8, now()))
		 RETURNING id, last_used_at, created_at`,
		nilIfZero(rs.ID), rs.UserID, rs.OrgID, rs.TokenHash, rs.UserAgent, rs.IP, rs.ExpiresAt,
		nilIfZeroTime(rs.LastUsed))
	return dbErr("create refresh session", row.Scan(&rs.ID, &rs.LastUsed, &rs.CreatedAt))
}

// GetRefreshSessionByHash returns the session currently holding tokenHash
// (revoked or expired ones included). A hash that was rotated away no longer
// matches: ErrNotFound.
func (s *Store) GetRefreshSessionByHash(ctx context.Context, tokenHash string) (*domain.RefreshSession, error) {
	rs, err := scanRefreshSession(s.db.QueryRow(ctx,
		`SELECT `+refreshSessionCols+` FROM refresh_sessions WHERE token_hash = $1`, tokenHash))
	return rs, dbErr("get refresh session by hash", err)
}

// RotateRefreshSession atomically replaces the token hash and expiry of a
// live session and bumps last_used_at. ErrConflict when the session is
// revoked or expired (or newHash is already taken), ErrNotFound when id does
// not exist.
func (s *Store) RotateRefreshSession(ctx context.Context, id uuid.UUID, newHash string, expiresAt time.Time) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE refresh_sessions SET token_hash = $2, expires_at = $3, last_used_at = now()
		 WHERE id = $1 AND revoked_at IS NULL AND expires_at > now()`,
		id, newHash, expiresAt)
	if err != nil {
		return dbErr("rotate refresh session", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refresh_sessions WHERE id = $1)`, id).
		Scan(&exists); err != nil {
		return dbErr("rotate refresh session", err)
	}
	if !exists {
		return fmt.Errorf("crm: rotate refresh session: %w", domain.ErrNotFound)
	}
	return fmt.Errorf("crm: rotate refresh session: revoked or expired: %w", domain.ErrConflict)
}

// RevokeRefreshSession revokes one session (idempotent; the first revocation
// time is kept). ErrNotFound when id does not exist.
func (s *Store) RevokeRefreshSession(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE refresh_sessions SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1`, id)
	return affected("revoke refresh session", tag, err)
}

// RevokeUserSessions revokes every live session of a user (logout
// everywhere, password change, disable).
func (s *Store) RevokeUserSessions(ctx context.Context, userID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE refresh_sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return dbErr("revoke user sessions", err)
}

// ListRefreshSessions lists a user's live (not revoked, not expired)
// sessions, most recently used first.
func (s *Store) ListRefreshSessions(ctx context.Context, userID uuid.UUID) ([]domain.RefreshSession, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+refreshSessionCols+` FROM refresh_sessions
		 WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		 ORDER BY last_used_at DESC, id`, userID)
	if err != nil {
		return nil, dbErr("list refresh sessions", err)
	}
	return collect("list refresh sessions", rows, scanRefreshSession)
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

const apiKeyCols = `id, org_id, name, prefix, key_hash, scopes, created_by, last_used_at, revoked_at, created_at`

func scanAPIKey(row pgx.Row) (*domain.APIKey, error) {
	var k domain.APIKey
	if err := row.Scan(&k.ID, &k.OrgID, &k.Name, &k.Prefix, &k.KeyHash, &k.Scopes, &k.CreatedBy,
		&k.LastUsedAt, &k.RevokedAt, &k.CreatedAt); err != nil {
		return nil, err
	}
	k.Scopes = nonNilStrings(k.Scopes)
	return &k, nil
}

// CreateAPIKey inserts a key. A duplicate prefix is ErrConflict (the caller
// regenerates).
func (s *Store) CreateAPIKey(ctx context.Context, k *domain.APIKey) error {
	k.Scopes = nonNilStrings(k.Scopes)
	row := s.db.QueryRow(ctx,
		`INSERT INTO api_keys (id, org_id, name, prefix, key_hash, scopes, created_by)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at`,
		nilIfZero(k.ID), k.OrgID, k.Name, k.Prefix, k.KeyHash, k.Scopes, k.CreatedBy)
	return dbErr("create api key", row.Scan(&k.ID, &k.CreatedAt))
}

// GetAPIKeyByPrefix returns the key with prefix (revoked ones included; the
// caller checks RevokedAt and compares KeyHash in constant time).
func (s *Store) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRow(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE prefix = $1`, prefix))
	return k, dbErr("get api key by prefix", err)
}

// ListAPIKeys lists an organisation's keys, revoked included, newest first.
func (s *Store) ListAPIKeys(ctx context.Context, orgID uuid.UUID) ([]domain.APIKey, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE org_id = $1 ORDER BY created_at DESC, id`, orgID)
	if err != nil {
		return nil, dbErr("list api keys", err)
	}
	return collect("list api keys", rows, scanAPIKey)
}

// RevokeAPIKey revokes a key (idempotent; the first revocation time is
// kept). ErrNotFound when id does not exist.
func (s *Store) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1`, id)
	return affected("revoke api key", tag, err)
}

// TouchAPIKey records a use of the key.
func (s *Store) TouchAPIKey(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
	return affected("touch api key", tag, err)
}

// ---------------------------------------------------------------------------
// Audit log
// ---------------------------------------------------------------------------

const auditCols = `id, org_id, actor_id, actor_email, action, target_type, target_id, meta, ip, at`

func scanAudit(row pgx.Row) (*domain.AuditEntry, error) {
	var e domain.AuditEntry
	if err := row.Scan(&e.ID, &e.OrgID, &e.ActorID, &e.ActorEmail, &e.Action, &e.TargetType, &e.TargetID,
		&e.Meta, &e.IP, &e.At); err != nil {
		return nil, err
	}
	if len(e.Meta) == 0 {
		e.Meta = nil
	}
	return &e, nil
}

// AppendAudit records an entry. At defaults to now.
func (s *Store) AppendAudit(ctx context.Context, e *domain.AuditEntry) error {
	meta, err := jsonObject(e.Meta)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO audit_log (id, org_id, actor_id, actor_email, action, target_type, target_id, meta, ip, at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, now()))
		 RETURNING id, at`,
		nilIfZero(e.ID), e.OrgID, e.ActorID, e.ActorEmail, e.Action, e.TargetType, e.TargetID, meta, e.IP,
		nilIfZeroTime(e.At))
	return dbErr("append audit", row.Scan(&e.ID, &e.At))
}

// ListAudit pages through an organisation's audit log, newest first. Filters:
// ActorID, Action (exact, or a prefix when it ends with "." or "*", e.g.
// "user." / "user.*"), From (inclusive) and To (exclusive). Limit defaults to
// 50 (max 500). total counts all matches.
func (s *Store) ListAudit(ctx context.Context, orgID uuid.UUID, f domain.AuditFilter) ([]domain.AuditEntry, int, error) {
	limit, offset := clampPage(f.Limit, f.Offset, 50, 500)
	conds, args := []string{"org_id = $1"}, []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.ActorID != nil {
		add("actor_id = $%d", *f.ActorID)
	}
	if a := strings.TrimSpace(f.Action); a != "" {
		if p, ok := strings.CutSuffix(a, "*"); ok {
			add(`action LIKE $%d`, likePrefix(p))
		} else if strings.HasSuffix(a, ".") {
			add(`action LIKE $%d`, likePrefix(a))
		} else {
			add("action = $%d", a)
		}
	}
	if f.From != nil {
		add("at >= $%d", *f.From)
	}
	if f.To != nil {
		add("at < $%d", *f.To)
	}
	where := strings.Join(conds, " AND ")

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, dbErr("list audit: count", err)
	}
	n := len(args)
	rows, err := s.db.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM audit_log WHERE %s ORDER BY at DESC, id LIMIT $%d OFFSET $%d`,
			auditCols, where, n+1, n+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, dbErr("list audit", err)
	}
	entries, err := collect("list audit", rows, scanAudit)
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// likePrefix escapes LIKE metacharacters in p and appends %.
func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}
