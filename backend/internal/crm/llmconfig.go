package crm

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const llmConfigCols = `id, org_id, name, provider, model, base_url, api_key_enc, api_key_hint, temperature,
	max_tokens, is_default, fallback_id, created_at, updated_at`

// llmRow carries the encrypted key alongside the entity until decided whether
// to decrypt it.
type llmRow struct {
	cfg domain.LLMConfig
	enc string
}

func scanLLMRow(row pgx.Row) (*llmRow, error) {
	var r llmRow
	c := &r.cfg
	err := row.Scan(&c.ID, &c.OrgID, &c.Name, &c.Provider, &c.Model, &c.BaseURL, &r.enc, &c.APIKeyHint,
		&c.Temperature, &c.MaxTokens, &c.IsDefault, &c.FallbackID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) decryptRow(op string, r *llmRow) (*domain.LLMConfig, error) {
	key, err := s.cipher.decrypt(r.enc)
	if err != nil {
		return nil, fmt.Errorf("crm: %s %s: %w", op, r.cfg.ID, err)
	}
	c := r.cfg
	c.APIKey = key
	return &c, nil
}

// CreateLLMConfig inserts a model configuration, encrypting APIKey. When
// IsDefault is set, any previous default of the organisation is cleared.
func (s *Store) CreateLLMConfig(ctx context.Context, c *domain.LLMConfig) error {
	enc, err := s.cipher.encrypt(c.APIKey)
	if err != nil {
		return err
	}
	hint := apiKeyHint(c.APIKey)
	return s.inTx(ctx, func(tx *Store) error {
		if c.IsDefault {
			if err := tx.clearDefaultLLM(ctx, c.OrgID, uuid.Nil); err != nil {
				return err
			}
		}
		row := tx.db.QueryRow(ctx,
			`INSERT INTO llm_configs (id, org_id, name, provider, model, base_url, api_key_enc, api_key_hint,
				temperature, max_tokens, is_default, fallback_id)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			 RETURNING id, api_key_hint, created_at, updated_at`,
			nilIfZero(c.ID), c.OrgID, c.Name, string(c.Provider), c.Model, c.BaseURL, enc, hint,
			c.Temperature, c.MaxTokens, c.IsDefault, c.FallbackID)
		return dbErr("create llm config", row.Scan(&c.ID, &c.APIKeyHint, &c.CreatedAt, &c.UpdatedAt))
	})
}

// UpdateLLMConfig overwrites all mutable fields. An empty APIKey keeps the
// stored key (the browser never sees the plaintext to send it back).
func (s *Store) UpdateLLMConfig(ctx context.Context, c *domain.LLMConfig) error {
	var enc, hint *string
	if c.APIKey != "" {
		e, err := s.cipher.encrypt(c.APIKey)
		if err != nil {
			return err
		}
		h := apiKeyHint(c.APIKey)
		enc, hint = &e, &h
	}
	return s.inTx(ctx, func(tx *Store) error {
		if c.IsDefault {
			var orgID uuid.UUID
			if err := tx.db.QueryRow(ctx, `SELECT org_id FROM llm_configs WHERE id = $1`, c.ID).Scan(&orgID); err != nil {
				return dbErr("update llm config", err)
			}
			if err := tx.clearDefaultLLM(ctx, orgID, c.ID); err != nil {
				return err
			}
		}
		row := tx.db.QueryRow(ctx,
			`UPDATE llm_configs SET name = $2, provider = $3, model = $4, base_url = $5,
				api_key_enc = COALESCE($6, api_key_enc), api_key_hint = COALESCE($7, api_key_hint),
				temperature = $8, max_tokens = $9, is_default = $10, fallback_id = $11, updated_at = now()
			 WHERE id = $1
			 RETURNING org_id, api_key_hint, created_at, updated_at`,
			c.ID, c.Name, string(c.Provider), c.Model, c.BaseURL, enc, hint, c.Temperature, c.MaxTokens,
			c.IsDefault, c.FallbackID)
		return dbErr("update llm config", row.Scan(&c.OrgID, &c.APIKeyHint, &c.CreatedAt, &c.UpdatedAt))
	})
}

func (s *Store) clearDefaultLLM(ctx context.Context, orgID, except uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE llm_configs SET is_default = false, updated_at = now()
		 WHERE org_id = $1 AND is_default AND id <> $2`, orgID, except)
	return dbErr("clear default llm config", err)
}

// DeleteLLMConfig removes a config; profiles and fallbacks pointing to it are
// set to NULL.
func (s *Store) DeleteLLMConfig(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM llm_configs WHERE id = $1`, id)
	return affected("delete llm config", tag, err)
}

// GetLLMConfig returns a config with APIKey decrypted.
func (s *Store) GetLLMConfig(ctx context.Context, id uuid.UUID) (*domain.LLMConfig, error) {
	r, err := scanLLMRow(s.db.QueryRow(ctx, `SELECT `+llmConfigCols+` FROM llm_configs WHERE id = $1`, id))
	if err != nil {
		return nil, dbErr("get llm config", err)
	}
	return s.decryptRow("get llm config", r)
}

// GetDefaultLLMConfig returns the organisation's default config (APIKey
// decrypted). If none is flagged default, the oldest config is returned;
// ErrNotFound when the organisation has no config at all.
func (s *Store) GetDefaultLLMConfig(ctx context.Context, orgID uuid.UUID) (*domain.LLMConfig, error) {
	r, err := scanLLMRow(s.db.QueryRow(ctx,
		`SELECT `+llmConfigCols+` FROM llm_configs WHERE org_id = $1
		 ORDER BY is_default DESC, created_at, id LIMIT 1`, orgID))
	if err != nil {
		return nil, dbErr("get default llm config", err)
	}
	return s.decryptRow("get default llm config", r)
}

// ListLLMConfigs lists configs WITHOUT decrypted keys (APIKey empty, only
// APIKeyHint), default first.
func (s *Store) ListLLMConfigs(ctx context.Context, orgID uuid.UUID) ([]domain.LLMConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+llmConfigCols+` FROM llm_configs WHERE org_id = $1 ORDER BY is_default DESC, created_at, name`, orgID)
	if err != nil {
		return nil, dbErr("list llm configs", err)
	}
	list, err := collect("list llm configs", rows, scanLLMRow)
	if err != nil {
		return nil, err
	}
	out := make([]domain.LLMConfig, len(list))
	for i := range list {
		out[i] = list[i].cfg
	}
	return out, nil
}
