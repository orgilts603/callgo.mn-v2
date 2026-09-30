package crm

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Default tenant created by EnsureDefaultOrg / SeedDemo.
const (
	DefaultOrgSlug = "demo"
	DefaultOrgName = "CallGo Demo"
)

const orgCols = `id, name, slug, created_at`

func scanOrg(row pgx.Row) (*domain.Organization, error) {
	var o domain.Organization
	if err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt); err != nil {
		return nil, err
	}
	return &o, nil
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

// CreateOrg inserts an organisation (used by self-service signup). Not part of
// domain.OrgRepository; exposed for the auth service via a local interface.
func (s *Store) CreateOrg(ctx context.Context, o *domain.Organization) error {
	row := s.db.QueryRow(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES (COALESCE($1, gen_random_uuid()), $2, $3)
		 RETURNING id, created_at`,
		nilIfZero(o.ID), o.Name, o.Slug)
	return dbErr("create org", row.Scan(&o.ID, &o.CreatedAt))
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

const userCols = `id, org_id, email, name, role, password_hash, created_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	if err := row.Scan(&u.ID, &u.OrgID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a user. E-mails are unique case-insensitively across all
// organisations (ErrConflict otherwise). Role defaults to operator.
func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	u.Email = strings.TrimSpace(u.Email)
	if u.Role == "" {
		u.Role = domain.RoleOperator
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO users (id, org_id, email, name, role, password_hash)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6)
		 RETURNING id, created_at`,
		nilIfZero(u.ID), u.OrgID, u.Email, u.Name, string(u.Role), u.PasswordHash)
	return dbErr("create user", row.Scan(&u.ID, &u.CreatedAt))
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

// CountUsers returns the number of users across all organisations (used by the
// dev bootstrap "seed if no users exist"). Not part of the domain port.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, dbErr("count users", err)
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
