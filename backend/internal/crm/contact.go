package crm

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	defaultContactLimit = 50
	maxContactLimit     = 1000
)

const contactCols = `id, org_id, phone, name, tags, meta, created_at, updated_at`

func scanContact(row pgx.Row) (*domain.Contact, error) {
	var c domain.Contact
	if err := row.Scan(&c.ID, &c.OrgID, &c.Phone, &c.Name, &c.Tags, &c.Meta, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	if c.Meta == nil {
		c.Meta = map[string]string{}
	}
	return &c, nil
}

func dedupeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// UpsertContact inserts a contact or merges into the existing one with the
// same (org, phone): a non-empty name replaces the old one, tags are unioned
// (existing order first) and meta keys are merged (new values win). The
// passed struct is overwritten with the stored row (ID, merged fields,
// timestamps).
func (s *Store) UpsertContact(ctx context.Context, c *domain.Contact) error {
	c.Phone = strings.TrimSpace(c.Phone)
	meta, err := jsonObject(c.Meta)
	if err != nil {
		return err
	}
	stored, err := scanContact(s.db.QueryRow(ctx,
		`INSERT INTO contacts AS ct (id, org_id, phone, name, tags, meta)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6)
		 ON CONFLICT (org_id, phone) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE ct.name END,
			tags = ct.tags || ARRAY(SELECT t FROM unnest(excluded.tags) WITH ORDINALITY AS x(t, i)
			                        WHERE NOT (t = ANY(ct.tags)) ORDER BY i),
			meta = ct.meta || excluded.meta,
			updated_at = now()
		 RETURNING `+contactCols,
		nilIfZero(c.ID), c.OrgID, c.Phone, strings.TrimSpace(c.Name), dedupeTags(c.Tags), meta))
	if err != nil {
		return dbErr("upsert contact", err)
	}
	*c = *stored
	return nil
}

// GetContact returns a contact by ID.
func (s *Store) GetContact(ctx context.Context, id uuid.UUID) (*domain.Contact, error) {
	c, err := scanContact(s.db.QueryRow(ctx, `SELECT `+contactCols+` FROM contacts WHERE id = $1`, id))
	return c, dbErr("get contact", err)
}

// GetContactByPhone returns an organisation's contact by exact phone.
func (s *Store) GetContactByPhone(ctx context.Context, orgID uuid.UUID, phone string) (*domain.Contact, error) {
	c, err := scanContact(s.db.QueryRow(ctx,
		`SELECT `+contactCols+` FROM contacts WHERE org_id = $1 AND phone = $2`, orgID, strings.TrimSpace(phone)))
	return c, dbErr("get contact by phone", err)
}

// ListContacts pages through contacts (most recently updated first); search
// matches name or phone case-insensitively.
func (s *Store) ListContacts(ctx context.Context, orgID uuid.UUID, search string, limit, offset int) ([]domain.Contact, int, error) {
	limit, offset = clampPage(limit, offset, defaultContactLimit, maxContactLimit)
	var args argList
	where := "org_id = " + args.add(orgID)
	if q := strings.TrimSpace(search); q != "" {
		p := args.add(likePattern(q))
		where += " AND (name ILIKE " + p + " OR phone ILIKE " + p + ")"
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, dbErr("count contacts", err)
	}
	if total == 0 || offset >= total {
		return []domain.Contact{}, total, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+contactCols+` FROM contacts WHERE `+where+
			` ORDER BY updated_at DESC, id LIMIT `+args.add(limit)+` OFFSET `+args.add(offset), args...)
	if err != nil {
		return nil, 0, dbErr("list contacts", err)
	}
	list, err := collect("list contacts", rows, scanContact)
	return list, total, err
}

// DeleteContact removes a contact; its calls and campaign targets keep a NULL
// reference.
func (s *Store) DeleteContact(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM contacts WHERE id = $1`, id)
	return affected("delete contact", tag, err)
}
