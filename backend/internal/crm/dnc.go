package crm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Do-not-call listing page sizes.
const (
	defaultDNCLimit = 50
	maxDNCLimit     = 1000
)

const dncCols = `id, org_id, phone, reason, created_by, created_at`

func scanDNC(row pgx.Row) (*domain.DoNotCallEntry, error) {
	var e domain.DoNotCallEntry
	if err := row.Scan(&e.ID, &e.OrgID, &e.Phone, &e.Reason, &e.CreatedBy, &e.CreatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

// AddDoNotCall puts a number on the organisation's do-not-call list. It is
// idempotent on (org, phone): when the number is already listed, e is
// overwritten with the existing entry and no error is returned.
func (s *Store) AddDoNotCall(ctx context.Context, e *domain.DoNotCallEntry) error {
	_, err := s.InsertDoNotCall(ctx, e)
	return err
}

// InsertDoNotCall is AddDoNotCall that also reports whether a new entry was
// created (false: the number was already listed and e now holds the existing
// entry). Not part of the domain port; lets POST /api/dnc answer 201 vs 200.
func (s *Store) InsertDoNotCall(ctx context.Context, e *domain.DoNotCallEntry) (bool, error) {
	e.Phone = strings.TrimSpace(e.Phone)
	if e.Phone == "" {
		return false, fmt.Errorf("crm: add do-not-call: empty phone: %w", domain.ErrInvalid)
	}
	e.Reason = strings.TrimSpace(e.Reason)
	created, err := scanDNC(s.db.QueryRow(ctx,
		`INSERT INTO do_not_call (id, org_id, phone, reason, created_by)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5)
		 ON CONFLICT (org_id, phone) DO NOTHING
		 RETURNING `+dncCols,
		nilIfZero(e.ID), e.OrgID, e.Phone, e.Reason, e.CreatedBy))
	if err == nil {
		*e = *created
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, dbErr("add do-not-call", err)
	}
	existing, err := scanDNC(s.db.QueryRow(ctx,
		`SELECT `+dncCols+` FROM do_not_call WHERE org_id = $1 AND phone = $2`, e.OrgID, e.Phone))
	if err != nil {
		return false, dbErr("add do-not-call: read existing", err)
	}
	*e = *existing
	return false, nil
}

// RemoveDoNotCall takes a number off the list; ErrNotFound when not listed.
func (s *Store) RemoveDoNotCall(ctx context.Context, orgID uuid.UUID, phone string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM do_not_call WHERE org_id = $1 AND phone = $2`,
		orgID, strings.TrimSpace(phone))
	return affected("remove do-not-call", tag, err)
}

// ListDoNotCall pages through an organisation's list, newest first. search
// matches phone or reason (case-insensitive substring). It also returns the
// total number of matches.
func (s *Store) ListDoNotCall(ctx context.Context, orgID uuid.UUID, search string, limit, offset int) ([]domain.DoNotCallEntry, int, error) {
	limit, offset = clampPage(limit, offset, defaultDNCLimit, maxDNCLimit)
	var args argList
	where := "org_id = " + args.add(orgID)
	if q := strings.TrimSpace(search); q != "" {
		p := args.add(likePattern(q))
		where += " AND (phone ILIKE " + p + " OR reason ILIKE " + p + ")"
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM do_not_call WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, dbErr("count do-not-call", err)
	}
	if total == 0 || offset >= total {
		return []domain.DoNotCallEntry{}, total, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+dncCols+` FROM do_not_call WHERE `+where+
			` ORDER BY created_at DESC, id LIMIT `+args.add(limit)+` OFFSET `+args.add(offset), args...)
	if err != nil {
		return nil, 0, dbErr("list do-not-call", err)
	}
	list, err := collect("list do-not-call", rows, scanDNC)
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// IsDoNotCall reports whether phone is on the organisation's list.
func (s *Store) IsDoNotCall(ctx context.Context, orgID uuid.UUID, phone string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM do_not_call WHERE org_id = $1 AND phone = $2)`,
		orgID, strings.TrimSpace(phone)).Scan(&ok)
	return ok, dbErr("is do-not-call", err)
}

// FilterDoNotCall returns the subset of phones that are on the list (keys as
// passed in, surrounding whitespace ignored for matching).
func (s *Store) FilterDoNotCall(ctx context.Context, orgID uuid.UUID, phones []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(phones) == 0 {
		return out, nil
	}
	byTrimmed := make(map[string][]string, len(phones))
	query := make([]string, 0, len(phones))
	for _, p := range phones {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if _, seen := byTrimmed[t]; !seen {
			query = append(query, t)
		}
		byTrimmed[t] = append(byTrimmed[t], p)
	}
	if len(query) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT phone FROM do_not_call WHERE org_id = $1 AND phone = ANY($2::text[])`, orgID, query)
	if err != nil {
		return nil, dbErr("filter do-not-call", err)
	}
	defer rows.Close()
	for rows.Next() {
		var phone string
		if err := rows.Scan(&phone); err != nil {
			return nil, dbErr("filter do-not-call", err)
		}
		for _, orig := range byTrimmed[phone] {
			out[orig] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr("filter do-not-call", err)
	}
	return out, nil
}
