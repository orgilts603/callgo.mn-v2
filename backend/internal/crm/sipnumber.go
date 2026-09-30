package crm

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const sipNumberCols = `id, org_id, number, label, inbound_trunk_id, outbound_trunk_id, dispatch_rule_id,
	asterisk_endpoint, agent_profile_id, allow_inbound, allow_outbound, active, created_at, updated_at`

func scanSIPNumber(row pgx.Row) (*domain.SIPNumber, error) {
	var n domain.SIPNumber
	err := row.Scan(&n.ID, &n.OrgID, &n.Number, &n.Label, &n.InboundTrunkID, &n.OutboundTrunkID,
		&n.DispatchRuleID, &n.AsteriskEndpoint, &n.AgentProfileID, &n.AllowInbound, &n.AllowOutbound,
		&n.Active, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// CreateSIPNumber inserts a number (globally unique → ErrConflict).
func (s *Store) CreateSIPNumber(ctx context.Context, n *domain.SIPNumber) error {
	n.Number = strings.TrimSpace(n.Number)
	row := s.db.QueryRow(ctx,
		`INSERT INTO sip_numbers (id, org_id, number, label, inbound_trunk_id, outbound_trunk_id,
			dispatch_rule_id, asterisk_endpoint, agent_profile_id, allow_inbound, allow_outbound, active)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, created_at, updated_at`,
		nilIfZero(n.ID), n.OrgID, n.Number, n.Label, n.InboundTrunkID, n.OutboundTrunkID,
		n.DispatchRuleID, n.AsteriskEndpoint, n.AgentProfileID, n.AllowInbound, n.AllowOutbound, n.Active)
	return dbErr("create sip number", row.Scan(&n.ID, &n.CreatedAt, &n.UpdatedAt))
}

// UpdateSIPNumber overwrites all mutable fields (org is immutable).
func (s *Store) UpdateSIPNumber(ctx context.Context, n *domain.SIPNumber) error {
	n.Number = strings.TrimSpace(n.Number)
	row := s.db.QueryRow(ctx,
		`UPDATE sip_numbers SET number = $2, label = $3, inbound_trunk_id = $4, outbound_trunk_id = $5,
			dispatch_rule_id = $6, asterisk_endpoint = $7, agent_profile_id = $8, allow_inbound = $9,
			allow_outbound = $10, active = $11, updated_at = now()
		 WHERE id = $1
		 RETURNING org_id, created_at, updated_at`,
		n.ID, n.Number, n.Label, n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID,
		n.AsteriskEndpoint, n.AgentProfileID, n.AllowInbound, n.AllowOutbound, n.Active)
	return dbErr("update sip number", row.Scan(&n.OrgID, &n.CreatedAt, &n.UpdatedAt))
}

// DeleteSIPNumber removes a number; calls/campaigns keep a NULL reference.
func (s *Store) DeleteSIPNumber(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM sip_numbers WHERE id = $1`, id)
	return affected("delete sip number", tag, err)
}

// GetSIPNumber returns a number by ID.
func (s *Store) GetSIPNumber(ctx context.Context, id uuid.UUID) (*domain.SIPNumber, error) {
	n, err := scanSIPNumber(s.db.QueryRow(ctx, `SELECT `+sipNumberCols+` FROM sip_numbers WHERE id = $1`, id))
	return n, dbErr("get sip number", err)
}

// GetSIPNumberByNumber resolves a DID in any organisation.
func (s *Store) GetSIPNumberByNumber(ctx context.Context, number string) (*domain.SIPNumber, error) {
	n, err := scanSIPNumber(s.db.QueryRow(ctx,
		`SELECT `+sipNumberCols+` FROM sip_numbers WHERE number = $1`, strings.TrimSpace(number)))
	return n, dbErr("get sip number by number", err)
}

// ListSIPNumbers lists an organisation's numbers ordered by number.
func (s *Store) ListSIPNumbers(ctx context.Context, orgID uuid.UUID) ([]domain.SIPNumber, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+sipNumberCols+` FROM sip_numbers WHERE org_id = $1 ORDER BY number`, orgID)
	if err != nil {
		return nil, dbErr("list sip numbers", err)
	}
	return collect("list sip numbers", rows, scanSIPNumber)
}
