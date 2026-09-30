package crm

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const lexiconCols = `id, org_id, wrong, correct, phonetic, scope, source_turn_id, created_by, hit_count, created_at`

func scanCorrection(row pgx.Row) (*domain.LexiconCorrection, error) {
	var c domain.LexiconCorrection
	err := row.Scan(&c.ID, &c.OrgID, &c.Wrong, &c.Correct, &c.Phonetic, &c.Scope, &c.SourceTurn,
		&c.CreatedBy, &c.HitCount, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// AddCorrection inserts a correction. A second correction for the same wrong
// word (case-insensitive) in the organisation yields ErrConflict. Scope
// defaults to "stt".
func (s *Store) AddCorrection(ctx context.Context, c *domain.LexiconCorrection) error {
	c.Wrong = strings.TrimSpace(c.Wrong)
	c.Correct = strings.TrimSpace(c.Correct)
	if c.Scope == "" {
		c.Scope = domain.ScopeSTT
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO lexicon_corrections (id, org_id, wrong, correct, phonetic, scope, source_turn_id,
			created_by, hit_count)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		nilIfZero(c.ID), c.OrgID, c.Wrong, c.Correct, c.Phonetic, string(c.Scope), c.SourceTurn,
		c.CreatedBy, c.HitCount)
	return dbErr("add correction", row.Scan(&c.ID, &c.CreatedAt))
}

// UpdateCorrection changes wrong/correct/phonetic/scope; the struct is
// refreshed from the stored row (org, source, creator, hits, created_at).
func (s *Store) UpdateCorrection(ctx context.Context, c *domain.LexiconCorrection) error {
	c.Wrong = strings.TrimSpace(c.Wrong)
	c.Correct = strings.TrimSpace(c.Correct)
	if c.Scope == "" {
		c.Scope = domain.ScopeSTT
	}
	stored, err := scanCorrection(s.db.QueryRow(ctx,
		`UPDATE lexicon_corrections SET wrong = $2, correct = $3, phonetic = $4, scope = $5, updated_at = now()
		 WHERE id = $1
		 RETURNING `+lexiconCols,
		c.ID, c.Wrong, c.Correct, c.Phonetic, string(c.Scope)))
	if err != nil {
		return dbErr("update correction", err)
	}
	*c = *stored
	return nil
}

// ListCorrections lists an organisation's corrections, newest first.
func (s *Store) ListCorrections(ctx context.Context, orgID uuid.UUID) ([]domain.LexiconCorrection, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+lexiconCols+` FROM lexicon_corrections WHERE org_id = $1 ORDER BY created_at DESC, id`, orgID)
	if err != nil {
		return nil, dbErr("list corrections", err)
	}
	return collect("list corrections", rows, scanCorrection)
}

// GetCorrection returns one correction (not part of the domain port).
func (s *Store) GetCorrection(ctx context.Context, id uuid.UUID) (*domain.LexiconCorrection, error) {
	c, err := scanCorrection(s.db.QueryRow(ctx, `SELECT `+lexiconCols+` FROM lexicon_corrections WHERE id = $1`, id))
	return c, dbErr("get correction", err)
}

// DeleteCorrection removes a correction.
func (s *Store) DeleteCorrection(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM lexicon_corrections WHERE id = $1`, id)
	return affected("delete correction", tag, err)
}

// IncrementHits adds one hit per occurrence of each ID (duplicates count
// multiple times). Unknown IDs are ignored.
func (s *Store) IncrementHits(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	_, err := s.db.Exec(ctx,
		`UPDATE lexicon_corrections l SET hit_count = l.hit_count + x.n
		 FROM (SELECT u::uuid AS id, count(*)::int AS n FROM unnest($1::text[]) AS u GROUP BY u::uuid) x
		 WHERE l.id = x.id`, strs)
	return dbErr("increment lexicon hits", err)
}
