package crm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultLanguage is applied to agent profiles created without a language.
const DefaultLanguage = "mn"

const agentProfileCols = `id, org_id, name, system_prompt, greeting, language, llm_config_id, stt_provider,
	stt_model, tts_provider, tts_voice, max_duration_sec, tools, transfer_number, knowledge_base_id, knowledge_mode,
	post_call_actions, created_at, updated_at`

func scanAgentProfile(row pgx.Row) (*domain.AgentProfile, error) {
	var p domain.AgentProfile
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.SystemPrompt, &p.Greeting, &p.Language, &p.LLMConfigID,
		&p.STTProvider, &p.STTModel, &p.TTSProvider, &p.TTSVoice, &p.MaxDurationSec, &p.Tools,
		&p.TransferNumber, &p.KnowledgeBaseID, &p.KnowledgeMode, &p.PostCallActions, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if p.Tools == nil {
		p.Tools = []string{}
	}
	if p.PostCallActions == nil {
		p.PostCallActions = []domain.PostCallAction{}
	}
	return &p, nil
}

// postCallActionsJSON renders actions for the agent_profiles.post_call_actions
// jsonb column; nil is stored as [].
func postCallActionsJSON(a []domain.PostCallAction) (string, error) {
	if len(a) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("crm: marshal post-call actions: %w", err)
	}
	return string(b), nil
}

// normalizeAgentProfile applies defaults: language "mn", knowledge mode "off",
// non-nil tools.
func normalizeAgentProfile(p *domain.AgentProfile) {
	if p.Language == "" {
		p.Language = DefaultLanguage
	}
	if p.KnowledgeMode == "" {
		p.KnowledgeMode = domain.KnowledgeOff
	}
	p.Tools = nonNilStrings(p.Tools)
}

// CreateAgentProfile inserts a persona. Language defaults to "mn" and
// KnowledgeMode to "off".
func (s *Store) CreateAgentProfile(ctx context.Context, p *domain.AgentProfile) error {
	normalizeAgentProfile(p)
	actions, err := postCallActionsJSON(p.PostCallActions)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO agent_profiles (id, org_id, name, system_prompt, greeting, language, llm_config_id,
			stt_provider, stt_model, tts_provider, tts_voice, max_duration_sec, tools, transfer_number,
			knowledge_base_id, knowledge_mode, post_call_actions)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			$16, $17)
		 RETURNING id, created_at, updated_at`,
		nilIfZero(p.ID), p.OrgID, p.Name, p.SystemPrompt, p.Greeting, p.Language, p.LLMConfigID,
		p.STTProvider, p.STTModel, p.TTSProvider, p.TTSVoice, p.MaxDurationSec, p.Tools, p.TransferNumber,
		p.KnowledgeBaseID, p.KnowledgeMode, actions)
	return dbErr("create agent profile", row.Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt))
}

// UpdateAgentProfile overwrites all mutable fields.
func (s *Store) UpdateAgentProfile(ctx context.Context, p *domain.AgentProfile) error {
	normalizeAgentProfile(p)
	actions, err := postCallActionsJSON(p.PostCallActions)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`UPDATE agent_profiles SET name = $2, system_prompt = $3, greeting = $4, language = $5,
			llm_config_id = $6, stt_provider = $7, stt_model = $8, tts_provider = $9, tts_voice = $10,
			max_duration_sec = $11, tools = $12, transfer_number = $13, knowledge_base_id = $14,
			knowledge_mode = $15, post_call_actions = $16, updated_at = now()
		 WHERE id = $1
		 RETURNING org_id, created_at, updated_at`,
		p.ID, p.Name, p.SystemPrompt, p.Greeting, p.Language, p.LLMConfigID, p.STTProvider, p.STTModel,
		p.TTSProvider, p.TTSVoice, p.MaxDurationSec, p.Tools, p.TransferNumber, p.KnowledgeBaseID, p.KnowledgeMode,
		actions)
	return dbErr("update agent profile", row.Scan(&p.OrgID, &p.CreatedAt, &p.UpdatedAt))
}

// DeleteAgentProfile removes a persona; numbers/calls/campaigns keep NULL.
func (s *Store) DeleteAgentProfile(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM agent_profiles WHERE id = $1`, id)
	return affected("delete agent profile", tag, err)
}

// GetAgentProfile returns a persona by ID.
func (s *Store) GetAgentProfile(ctx context.Context, id uuid.UUID) (*domain.AgentProfile, error) {
	p, err := scanAgentProfile(s.db.QueryRow(ctx,
		`SELECT `+agentProfileCols+` FROM agent_profiles WHERE id = $1`, id))
	return p, dbErr("get agent profile", err)
}

// ListAgentProfiles lists an organisation's personas, oldest first.
func (s *Store) ListAgentProfiles(ctx context.Context, orgID uuid.UUID) ([]domain.AgentProfile, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+agentProfileCols+` FROM agent_profiles WHERE org_id = $1 ORDER BY created_at, name`, orgID)
	if err != nil {
		return nil, dbErr("list agent profiles", err)
	}
	return collect("list agent profiles", rows, scanAgentProfile)
}
