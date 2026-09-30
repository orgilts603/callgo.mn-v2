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

// Default tenant created by EnsureDefaultOrg / SeedDemo.
const (
	DefaultOrgSlug = "demo"
	DefaultOrgName = "CallGo Demo"
)

// Defaults applied to new organisations and users (mirroring the column
// defaults of migration 000005).
const (
	DefaultPlanCode = "trial"
	DefaultTimezone = "Asia/Ulaanbaatar"
)

const orgCols = `id, name, slug, plan_code, status, timezone, settings, created_at, updated_at`

func scanOrg(row pgx.Row) (*domain.Organization, error) {
	var o domain.Organization
	if err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.PlanCode, &o.Status, &o.Timezone, &o.Settings,
		&o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	if len(o.Settings) == 0 {
		o.Settings = nil
	}
	return &o, nil
}

// validTimezone reports whether tz is an IANA zone both Go and Postgres know
// ("" means "use the default").
func validTimezone(tz string) error {
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil || strings.EqualFold(tz, "local") {
		return fmt.Errorf("crm: timezone %q: %w", tz, domain.ErrInvalid)
	}
	return nil
}

// EnsureDefaultOrg returns the "demo" organisation, creating it if missing.
func (s *Store) EnsureDefaultOrg(ctx context.Context) (*domain.Organization, error) {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) ON CONFLICT (slug) DO NOTHING`,
		DefaultOrgName, DefaultOrgSlug); err != nil {
		return nil, dbErr("ensure default org", err)
	}
	return s.GetOrgBySlug(ctx, DefaultOrgSlug)
}

// CreateOrg inserts an organisation (self-service signup). Empty PlanCode,
// Status and Timezone take the defaults (trial, active, Asia/Ulaanbaatar),
// which are written back into o together with ID and timestamps. A duplicate
// slug is ErrConflict.
func (s *Store) CreateOrg(ctx context.Context, o *domain.Organization) error {
	if o.PlanCode == "" {
		o.PlanCode = DefaultPlanCode
	}
	if o.Status == "" {
		o.Status = domain.OrgActive
	}
	if o.Timezone == "" {
		o.Timezone = DefaultTimezone
	}
	if err := validTimezone(o.Timezone); err != nil {
		return err
	}
	settings, err := jsonObject(o.Settings)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO organizations (id, name, slug, plan_code, status, timezone, settings)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7)
		 RETURNING id, created_at, updated_at`,
		nilIfZero(o.ID), o.Name, o.Slug, o.PlanCode, string(o.Status), o.Timezone, settings)
	return dbErr("create org", row.Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt))
}

// UpdateOrg writes name, plan_code, status, timezone and settings of o.ID.
// Empty strings keep the stored value; nil Settings keeps the stored settings
// while a non-nil map replaces them. The slug is immutable. o is refreshed
// from the stored row.
func (s *Store) UpdateOrg(ctx context.Context, o *domain.Organization) error {
	if err := validTimezone(o.Timezone); err != nil {
		return err
	}
	var settings *string
	if o.Settings != nil {
		js, err := jsonObject(o.Settings)
		if err != nil {
			return err
		}
		settings = &js
	}
	got, err := scanOrg(s.db.QueryRow(ctx,
		`UPDATE organizations SET
			name = COALESCE(NULLIF($2, ''), name),
			plan_code = COALESCE(NULLIF($3, ''), plan_code),
			status = COALESCE(NULLIF($4, ''), status),
			timezone = COALESCE(NULLIF($5, ''), timezone),
			settings = COALESCE($6::jsonb, settings),
			updated_at = now()
		 WHERE id = $1
		 RETURNING `+orgCols,
		o.ID, o.Name, o.PlanCode, string(o.Status), o.Timezone, settings))
	if err != nil {
		return dbErr("update org", err)
	}
	*o = *got
	return nil
}

// ListOrgs pages through all organisations (platform admin), newest first.
// search matches name or slug case-insensitively; total is the match count.
func (s *Store) ListOrgs(ctx context.Context, search string, limit, offset int) ([]domain.Organization, int, error) {
	limit, offset = clampPage(limit, offset, 50, 500)
	where, args := `TRUE`, []any{}
	if q := strings.TrimSpace(search); q != "" {
		where = `(name ILIKE $1 OR slug ILIKE $1)`
		args = append(args, likePattern(q))
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, dbErr("list orgs: count", err)
	}
	n := len(args)
	rows, err := s.db.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM organizations WHERE %s ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d`,
			orgCols, where, n+1, n+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, dbErr("list orgs", err)
	}
	orgs, err := collect("list orgs", rows, scanOrg)
	if err != nil {
		return nil, 0, err
	}
	return orgs, total, nil
}

// GetOrg returns an organisation by ID.
func (s *Store) GetOrg(ctx context.Context, id uuid.UUID) (*domain.Organization, error) {
	o, err := scanOrg(s.db.QueryRow(ctx, `SELECT `+orgCols+` FROM organizations WHERE id = $1`, id))
	return o, dbErr("get org", err)
}

// GetOrgBySlug returns an organisation by its unique slug.
func (s *Store) GetOrgBySlug(ctx context.Context, slug string) (*domain.Organization, error) {
	o, err := scanOrg(s.db.QueryRow(ctx, `SELECT `+orgCols+` FROM organizations WHERE slug = $1`, slug))
	return o, dbErr("get org by slug", err)
}

const userCols = `id, org_id, email, name, role, password_hash, status, email_verified_at, last_login_at,
	is_platform_admin, created_at, updated_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	if err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.Status,
		&u.EmailVerifiedAt, &u.LastLoginAt, &u.IsPlatformAdmin, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a user. E-mails are unique case-insensitively across all
// organisations (ErrConflict otherwise). Role defaults to operator and Status
// to active.
func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	u.Email = strings.TrimSpace(u.Email)
	if u.Role == "" {
		u.Role = domain.RoleOperator
	}
	if u.Status == "" {
		u.Status = domain.UserActive
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO users (id, org_id, email, name, role, password_hash, status, email_verified_at,
			last_login_at, is_platform_admin)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at, updated_at`,
		nilIfZero(u.ID), u.OrgID, u.Email, u.Name, string(u.Role), u.PasswordHash, string(u.Status),
		u.EmailVerifiedAt, u.LastLoginAt, u.IsPlatformAdmin)
	return dbErr("create user", row.Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt))
}

// UpdateUser writes name, role, status, password_hash, email_verified_at,
// last_login_at and is_platform_admin of u.ID. Empty Role, Status and
// PasswordHash keep the stored value; the nullable timestamps are written as
// given (nil clears them). E-mail and org are immutable. u is refreshed from
// the stored row.
func (s *Store) UpdateUser(ctx context.Context, u *domain.User) error {
	got, err := scanUser(s.db.QueryRow(ctx,
		`UPDATE users SET
			name = $2,
			role = COALESCE(NULLIF($3, ''), role),
			status = COALESCE(NULLIF($4, ''), status),
			password_hash = COALESCE(NULLIF($5, ''), password_hash),
			email_verified_at = $6,
			last_login_at = $7,
			is_platform_admin = $8,
			updated_at = now()
		 WHERE id = $1
		 RETURNING `+userCols,
		u.ID, u.Name, string(u.Role), string(u.Status), u.PasswordHash, u.EmailVerifiedAt, u.LastLoginAt,
		u.IsPlatformAdmin))
	if err != nil {
		return dbErr("update user", err)
	}
	*u = *got
	return nil
}

// GetUserByEmail looks a user up case-insensitively.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)`, strings.TrimSpace(email)))
	return u, dbErr("get user by email", err)
}

// GetUser returns a user by ID.
func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	u, err := scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
	return u, dbErr("get user", err)
}

// ListUsers lists an organisation's users, oldest first.
func (s *Store) ListUsers(ctx context.Context, orgID uuid.UUID) ([]domain.User, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+userCols+` FROM users WHERE org_id = $1 ORDER BY created_at, email`, orgID)
	if err != nil {
		return nil, dbErr("list users", err)
	}
	return collect("list users", rows, scanUser)
}

// CountUsers returns the number of seats an organisation uses: its invited
// and active users (disabled users do not count against the plan limit).
func (s *Store) CountUsers(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE org_id = $1 AND status IN ('invited', 'active')`, orgID).Scan(&n)
	return n, dbErr("count users", err)
}

// CountAllUsers returns the number of users across all organisations (used by
// the dev bootstrap "seed if no users exist"). Not part of a domain port.
func (s *Store) CountAllUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, dbErr("count all users", err)
}

// collect drains rows with scan into a non-nil slice.
func collect[T any](op string, rows pgx.Rows, scan func(pgx.Row) (*T, error)) ([]T, error) {
	defer rows.Close()
	out := make([]T, 0)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, dbErr(op, err)
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(op, err)
	}
	return out, nil
}
