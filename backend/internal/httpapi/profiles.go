package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// allowedTools are the function tools the agent worker implements.
var allowedTools = []string{"end_call", "transfer_call", "lookup_contact", "schedule_callback"}

const (
	defaultLanguage       = "mn"
	defaultMaxDurationSec = 900
	maxMaxDurationSec     = 4 * 3600
)

func (s *server) loadProfile(ctx context.Context, orgID, id uuid.UUID) (*domain.AgentProfile, error) {
	p, err := s.d.AgentProfile.GetAgentProfile(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (p == nil || p.OrgID != orgID)) {
		return nil, errNotFound("agent profile")
	}
	if err != nil {
		return nil, fmt.Errorf("get agent profile: %w", err)
	}
	return p, nil
}

type profileBody struct {
	Name           string   `json:"name"`
	SystemPrompt   string   `json:"systemPrompt"`
	Greeting       string   `json:"greeting"`
	Language       string   `json:"language"`
	LLMConfigID    string   `json:"llmConfigId"`
	STTProvider    string   `json:"sttProvider"`
	STTModel       string   `json:"sttModel"`
	TTSProvider    string   `json:"ttsProvider"`
	TTSVoice       string   `json:"ttsVoice"`
	MaxDurationSec int      `json:"maxDurationSec"`
	Tools          []string `json:"tools"`
	TransferNumber string   `json:"transferNumber"`
}

func (s *server) applyProfileBody(ctx context.Context, b profileBody, p *domain.AgentProfile) error {
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return errInvalid("name is required")
	}
	if len(name) > 100 {
		return errInvalid("name must be at most 100 characters")
	}
	lang := strings.TrimSpace(b.Language)
	if lang == "" {
		lang = defaultLanguage
	}
	if len(lang) > 20 {
		return errInvalid("language must be a BCP-47 tag")
	}
	llmID, err := parseOptUUID(b.LLMConfigID, "llmConfigId")
	if err != nil {
		return err
	}
	if llmID != nil {
		if _, err := s.loadLLMConfig(ctx, p.OrgID, *llmID); err != nil {
			return asInvalidRef(err, "llmConfigId")
		}
	}
	maxDur := b.MaxDurationSec
	switch {
	case maxDur == 0:
		maxDur = defaultMaxDurationSec
	case maxDur < 10 || maxDur > maxMaxDurationSec:
		return errInvalid("maxDurationSec must be between 10 and %d", maxMaxDurationSec)
	}
	tools := make([]string, 0, len(b.Tools))
	for _, t := range b.Tools {
		t = strings.TrimSpace(t)
		if !slices.Contains(allowedTools, t) {
			return errInvalid("unknown tool %q (allowed: %s)", t, strings.Join(allowedTools, ", "))
		}
		if !slices.Contains(tools, t) {
			tools = append(tools, t)
		}
	}
	transfer := strings.TrimSpace(b.TransferNumber)
	if transfer != "" {
		if transfer, err = requirePhone(transfer, "transferNumber"); err != nil {
			return err
		}
	}
	p.Name, p.SystemPrompt, p.Greeting, p.Language = name, strings.TrimSpace(b.SystemPrompt), strings.TrimSpace(b.Greeting), lang
	p.LLMConfigID = llmID
	p.STTProvider, p.STTModel = strings.TrimSpace(b.STTProvider), strings.TrimSpace(b.STTModel)
	p.TTSProvider, p.TTSVoice = strings.TrimSpace(b.TTSProvider), strings.TrimSpace(b.TTSVoice)
	p.MaxDurationSec, p.Tools, p.TransferNumber = maxDur, tools, transfer
	return nil
}

func (s *server) listProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := s.d.AgentProfile.ListAgentProfiles(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list agent profiles: %w", err))
		return
	}
	for i := range items {
		if items[i].Tools == nil {
			items[i].Tools = []string{}
		}
	}
	writeJSON(w, http.StatusOK, newList(items, len(items)))
}

func (s *server) getProfile(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	p, err := s.loadProfile(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p})
}

func (s *server) createProfile(w http.ResponseWriter, r *http.Request) {
	var b profileBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	now := s.now()
	p := &domain.AgentProfile{ID: uuid.New(), OrgID: claimsOf(r).OrgID, CreatedAt: now, UpdatedAt: now}
	if err := s.applyProfileBody(ctx, b, p); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.AgentProfile.CreateAgentProfile(ctx, p); err != nil {
		s.writeErr(w, r, fmt.Errorf("create agent profile: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"profile": p})
}

func (s *server) updateProfile(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var b profileBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	p, err := s.loadProfile(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.applyProfileBody(ctx, b, p); err != nil {
		s.writeErr(w, r, err)
		return
	}
	p.UpdatedAt = s.now()
	if err := s.d.AgentProfile.UpdateAgentProfile(ctx, p); err != nil {
		s.writeErr(w, r, fmt.Errorf("update agent profile: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p})
}

func (s *server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.loadProfile(ctx, claimsOf(r).OrgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.AgentProfile.DeleteAgentProfile(ctx, id); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			s.writeErr(w, r, errConflict("agent profile is still in use"))
			return
		}
		s.writeErr(w, r, fmt.Errorf("delete agent profile: %w", err))
		return
	}
	noContent(w)
}
