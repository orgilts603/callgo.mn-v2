package crm

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	defaultTargetLimit = 100
	maxTargetLimit     = 1000
	defaultConcurrency = 2
	defaultMaxAttempts = 2
)

const campaignCols = `id, org_id, name, sip_number_id, agent_profile_id, script, status, concurrency,
	max_attempts, total, completed, failed, created_at, updated_at`

func scanCampaign(row pgx.Row) (*domain.Campaign, error) {
	var c domain.Campaign
	err := row.Scan(&c.ID, &c.OrgID, &c.Name, &c.SIPNumberID, &c.AgentProfileID, &c.Script, &c.Status,
		&c.Concurrency, &c.MaxAttempts, &c.Total, &c.Completed, &c.Failed, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

const targetCols = `id, campaign_id, contact_id, phone, name, vars, status, attempts, call_id, last_error,
	next_try_at, updated_at`

func scanTarget(row pgx.Row) (*domain.CampaignTarget, error) {
	var t domain.CampaignTarget
	err := row.Scan(&t.ID, &t.CampaignID, &t.ContactID, &t.Phone, &t.Name, &t.Vars, &t.Status, &t.Attempts,
		&t.CallID, &t.LastError, &t.NextTryAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(t.Vars) == 0 {
		t.Vars = nil
	}
	return &t, nil
}

func applyCampaignDefaults(c *domain.Campaign) {
	if c.Status == "" {
		c.Status = domain.CampaignDraft
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
}

// CreateCampaign inserts the campaign and all targets (via COPY) in one
// transaction. It sets c.Total = len(targets) and fills each target's ID,
// CampaignID, Status (pending unless set) and UpdatedAt in place. Target
// order is preserved for ListTargets.
func (s *Store) CreateCampaign(ctx context.Context, c *domain.Campaign, targets []domain.CampaignTarget) error {
	applyCampaignDefaults(c)
	c.Total = len(targets)

	return s.inTx(ctx, func(tx *Store) error {
		row := tx.db.QueryRow(ctx,
			`INSERT INTO campaigns (id, org_id, name, sip_number_id, agent_profile_id, script, status,
				concurrency, max_attempts, total, completed, failed)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			 RETURNING id, created_at, updated_at`,
			nilIfZero(c.ID), c.OrgID, c.Name, c.SIPNumberID, c.AgentProfileID, c.Script, string(c.Status),
			c.Concurrency, c.MaxAttempts, c.Total, c.Completed, c.Failed)
		if err := row.Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return dbErr("create campaign", err)
		}
		if len(targets) == 0 {
			return nil
		}

		rows := make([][]any, len(targets))
		for i := range targets {
			t := &targets[i]
			if t.ID == uuid.Nil {
				t.ID = uuid.New()
			}
			if t.Status == "" {
				t.Status = domain.TargetPending
			}
			t.CampaignID = c.ID
			t.Phone = strings.TrimSpace(t.Phone)
			t.UpdatedAt = c.CreatedAt
			vars, err := jsonObject(t.Vars)
			if err != nil {
				return err
			}
			rows[i] = []any{t.ID, c.ID, i, t.ContactID, t.Phone, t.Name, vars, string(t.Status),
				t.Attempts, t.CallID, t.LastError, t.NextTryAt}
		}
		n, err := tx.db.CopyFrom(ctx, pgx.Identifier{"campaign_targets"},
			[]string{"id", "campaign_id", "pos", "contact_id", "phone", "name", "vars", "status",
				"attempts", "call_id", "last_error", "next_try_at"},
			pgx.CopyFromRows(rows))
		if err != nil {
			return dbErr("create campaign targets", err)
		}
		if int(n) != len(targets) {
			return fmt.Errorf("crm: create campaign targets: copied %d of %d rows", n, len(targets))
		}
		return nil
	})
}

// UpdateCampaign overwrites all mutable fields including counters.
func (s *Store) UpdateCampaign(ctx context.Context, c *domain.Campaign) error {
	applyCampaignDefaults(c)
	row := s.db.QueryRow(ctx,
		`UPDATE campaigns SET name = $2, sip_number_id = $3, agent_profile_id = $4, script = $5, status = $6,
			concurrency = $7, max_attempts = $8, total = $9, completed = $10, failed = $11, updated_at = now()
		 WHERE id = $1
		 RETURNING org_id, created_at, updated_at`,
		c.ID, c.Name, c.SIPNumberID, c.AgentProfileID, c.Script, string(c.Status), c.Concurrency,
		c.MaxAttempts, c.Total, c.Completed, c.Failed)
	return dbErr("update campaign", row.Scan(&c.OrgID, &c.CreatedAt, &c.UpdatedAt))
}

// GetCampaign returns a campaign by ID.
func (s *Store) GetCampaign(ctx context.Context, id uuid.UUID) (*domain.Campaign, error) {
	c, err := scanCampaign(s.db.QueryRow(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE id = $1`, id))
	return c, dbErr("get campaign", err)
}

// ListCampaigns lists an organisation's campaigns, newest first.
func (s *Store) ListCampaigns(ctx context.Context, orgID uuid.UUID) ([]domain.Campaign, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+campaignCols+` FROM campaigns WHERE org_id = $1 ORDER BY created_at DESC, id`, orgID)
	if err != nil {
		return nil, dbErr("list campaigns", err)
	}
	return collect("list campaigns", rows, scanCampaign)
}

// ListRunningCampaigns lists running campaigns of every organisation (for the
// dialer engine), oldest first.
func (s *Store) ListRunningCampaigns(ctx context.Context) ([]domain.Campaign, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+campaignCols+` FROM campaigns WHERE status = 'running' ORDER BY created_at, id`)
	if err != nil {
		return nil, dbErr("list running campaigns", err)
	}
	return collect("list running campaigns", rows, scanCampaign)
}

// DeleteCampaign removes a campaign and its targets; its calls keep a NULL
// campaign reference. Not part of the domain port (DELETE /api/campaigns/{id});
// the status precondition is the service's job.
func (s *Store) DeleteCampaign(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM campaigns WHERE id = $1`, id)
	return affected("delete campaign", tag, err)
}

// RecountCampaign recomputes total/completed/failed from the targets and
// returns the refreshed campaign. Not part of the domain port.
func (s *Store) RecountCampaign(ctx context.Context, id uuid.UUID) (*domain.Campaign, error) {
	c, err := scanCampaign(s.db.QueryRow(ctx,
		`UPDATE campaigns cp SET
			total = x.total, completed = x.done, failed = x.failed, updated_at = now()
		 FROM (SELECT count(*) AS total,
		              count(*) FILTER (WHERE status = 'done') AS done,
		              count(*) FILTER (WHERE status = 'failed') AS failed
		       FROM campaign_targets WHERE campaign_id = $1) x
		 WHERE cp.id = $1
		 RETURNING cp.id, cp.org_id, cp.name, cp.sip_number_id, cp.agent_profile_id, cp.script, cp.status,
			cp.concurrency, cp.max_attempts, cp.total, cp.completed, cp.failed, cp.created_at, cp.updated_at`, id))
	return c, dbErr("recount campaign", err)
}

// ListTargets pages through a campaign's targets in upload order.
func (s *Store) ListTargets(ctx context.Context, campaignID uuid.UUID, limit, offset int) ([]domain.CampaignTarget, int, error) {
	limit, offset = clampPage(limit, offset, defaultTargetLimit, maxTargetLimit)
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM campaign_targets WHERE campaign_id = $1`, campaignID).
		Scan(&total); err != nil {
		return nil, 0, dbErr("count targets", err)
	}
	if total == 0 || offset >= total {
		return []domain.CampaignTarget{}, total, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+targetCols+` FROM campaign_targets WHERE campaign_id = $1 ORDER BY pos, id LIMIT $2 OFFSET $3`,
		campaignID, limit, offset)
	if err != nil {
		return nil, 0, dbErr("list targets", err)
	}
	list, err := collect("list targets", rows, scanTarget)
	return list, total, err
}

// ClaimTargets atomically moves up to n pending, due targets to "calling" and
// leaves Attempts unchanged (the campaign engine owns attempt accounting). Concurrent callers never receive the same target
// (FOR UPDATE SKIP LOCKED). Results are in upload order.
func (s *Store) ClaimTargets(ctx context.Context, campaignID uuid.UUID, n int) ([]domain.CampaignTarget, error) {
	if n <= 0 {
		return []domain.CampaignTarget{}, nil
	}
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
			UPDATE campaign_targets SET status = 'calling', updated_at = now()
			WHERE id IN (
				SELECT id FROM campaign_targets
				WHERE campaign_id = $1 AND status = 'pending' AND (next_try_at IS NULL OR next_try_at <= now())
				ORDER BY updated_at, pos
				LIMIT $2
				FOR UPDATE SKIP LOCKED)
			RETURNING pos, `+targetCols+`)
		 SELECT `+targetCols+` FROM claimed ORDER BY pos`,
		campaignID, n)
	if err != nil {
		return nil, dbErr("claim targets", err)
	}
	return collect("claim targets", rows, scanTarget)
}

// UpdateTarget overwrites a target's mutable fields (contact, phone, name,
// vars, status, attempts, call, error, next try).
func (s *Store) UpdateTarget(ctx context.Context, t *domain.CampaignTarget) error {
	vars, err := jsonObject(t.Vars)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`UPDATE campaign_targets SET contact_id = $2, phone = $3, name = $4, vars = $5, status = $6,
			attempts = $7, call_id = $8, last_error = $9, next_try_at = $10, updated_at = now()
		 WHERE id = $1
		 RETURNING campaign_id, updated_at`,
		t.ID, t.ContactID, strings.TrimSpace(t.Phone), t.Name, vars, string(t.Status), t.Attempts, t.CallID,
		t.LastError, t.NextTryAt)
	return dbErr("update target", row.Scan(&t.CampaignID, &t.UpdatedAt))
}

// CountActiveTargets counts targets currently being dialled (status
// "calling"), i.e. occupied concurrency slots.
func (s *Store) CountActiveTargets(ctx context.Context, campaignID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM campaign_targets WHERE campaign_id = $1 AND status = 'calling'`, campaignID).Scan(&n)
	return n, dbErr("count active targets", err)
}

// CountPendingTargets counts targets still waiting to be dialled (including
// retries not yet due). Not part of the domain port; lets the engine decide
// when a campaign is complete.
func (s *Store) CountPendingTargets(ctx context.Context, campaignID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM campaign_targets WHERE campaign_id = $1 AND status = 'pending'`, campaignID).Scan(&n)
	return n, dbErr("count pending targets", err)
}
