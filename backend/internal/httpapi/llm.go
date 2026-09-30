package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	defaultTemperature = 0.7
	defaultMaxTokens   = 1024
	maxMaxTokens       = 200000
	llmTestTimeout     = 45 * time.Second
	defaultTestPrompt  = "Reply with one short sentence to confirm you are working."
)

// catalogEntry is one provider in GET /api/llm-configs/catalog.
type catalogEntry struct {
	Provider     domain.LLMProvider `json:"provider"`
	Label        string             `json:"label"`
	Models       []string           `json:"models"`
	NeedsAPIKey  bool               `json:"needsApiKey"`
	NeedsBaseURL bool               `json:"needsBaseUrl"`
}

var llmCatalog = []catalogEntry{
	{domain.ProviderOpenAI, "OpenAI", []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4o-mini"}, true, false},
	{domain.ProviderAnthropic, "Anthropic", []string{"claude-sonnet-4-5", "claude-haiku-4-5"}, true, false},
	{domain.ProviderGoogle, "Google Gemini", []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash"}, true, false},
	{domain.ProviderGroq, "Groq", []string{"llama-3.3-70b-versatile", "qwen-qwq-32b"}, true, false},
	{domain.ProviderOllama, "Ollama (self-hosted)", []string{"llama3.1", "qwen2.5"}, false, true},
	{domain.ProviderOpenAICompatible, "OpenAI-compatible", []string{}, false, true},
}

func catalogFor(p domain.LLMProvider) (catalogEntry, bool) {
	for _, e := range llmCatalog {
		if e.Provider == p {
			return e, true
		}
	}
	return catalogEntry{}, false
}

func (s *server) llmCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": llmCatalog})
}

// loadLLMConfig returns the org's config with the APIKey decrypted.
func (s *server) loadLLMConfig(ctx context.Context, orgID, id uuid.UUID) (*domain.LLMConfig, error) {
	c, err := s.d.LLMConfig.GetLLMConfig(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (c == nil || c.OrgID != orgID)) {
		return nil, errNotFound("LLM config")
	}
	if err != nil {
		return nil, fmt.Errorf("get llm config: %w", err)
	}
	return c, nil
}

// public strips secrets before a config is sent to the browser.
func public(c *domain.LLMConfig) *domain.LLMConfig {
	cp := *c
	if cp.APIKeyHint == "" && cp.APIKey != "" {
		cp.APIKeyHint = keyHint(cp.APIKey)
	}
	cp.APIKey = ""
	return &cp
}

func keyHint(k string) string {
	if len(k) <= 8 {
		return "****"
	}
	return k[:3] + "..." + k[len(k)-4:]
}

type llmBody struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	BaseURL     string   `json:"baseUrl"`
	APIKey      string   `json:"apiKey"`
	Temperature *float32 `json:"temperature"`
	MaxTokens   *int     `json:"maxTokens"`
	IsDefault   bool     `json:"isDefault"`
	FallbackID  string   `json:"fallbackId"`
}

func (s *server) applyLLMBody(ctx context.Context, b llmBody, c *domain.LLMConfig, isNew bool) error {
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return errInvalid("name is required")
	}
	prov := domain.LLMProvider(strings.TrimSpace(b.Provider))
	entry, ok := catalogFor(prov)
	if !ok {
		return errInvalid("unknown provider %q", b.Provider)
	}
	model := strings.TrimSpace(b.Model)
	if model == "" {
		return errInvalid("model is required")
	}
	base := strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errInvalid("baseUrl must be an http(s) URL")
		}
	} else if entry.NeedsBaseURL {
		return errInvalid("baseUrl is required for provider %s", prov)
	}
	key := strings.TrimSpace(b.APIKey)
	if key == "" && !isNew {
		key = c.APIKey // keep the stored key
	}
	if key == "" && entry.NeedsAPIKey {
		return errInvalid("apiKey is required for provider %s", prov)
	}
	temp := float32(defaultTemperature)
	if !isNew {
		temp = c.Temperature
	}
	if b.Temperature != nil {
		temp = *b.Temperature
	}
	if temp < 0 || temp > 2 {
		return errInvalid("temperature must be between 0 and 2")
	}
	maxTok := defaultMaxTokens
	if !isNew && c.MaxTokens > 0 {
		maxTok = c.MaxTokens
	}
	if b.MaxTokens != nil && *b.MaxTokens != 0 {
		maxTok = *b.MaxTokens
	}
	if maxTok < 1 || maxTok > maxMaxTokens {
		return errInvalid("maxTokens must be between 1 and %d", maxMaxTokens)
	}
	fb, err := parseOptUUID(b.FallbackID, "fallbackId")
	if err != nil {
		return err
	}
	if fb != nil {
		if *fb == c.ID {
			return errInvalid("fallbackId must reference another config")
		}
		// Walk the chain to reject cycles.
		cur := *fb
		for range 10 {
			fc, err := s.loadLLMConfig(ctx, c.OrgID, cur)
			if err != nil {
				return asInvalidRef(err, "fallbackId")
			}
			if fc.FallbackID == nil {
				break
			}
			if *fc.FallbackID == c.ID {
				return errInvalid("fallbackId would create a cycle")
			}
			cur = *fc.FallbackID
		}
	}
	if key != c.APIKey {
		c.APIKeyHint = ""
		if key != "" {
			c.APIKeyHint = keyHint(key)
		}
	}
	c.Name, c.Provider, c.Model, c.BaseURL, c.APIKey = name, prov, model, base, key
	c.Temperature, c.MaxTokens, c.IsDefault, c.FallbackID = temp, maxTok, b.IsDefault, fb
	return nil
}

// makeSoleDefault clears isDefault on every other config of the org.
func (s *server) makeSoleDefault(ctx context.Context, c *domain.LLMConfig) error {
	all, err := s.d.LLMConfig.ListLLMConfigs(ctx, c.OrgID)
	if err != nil {
		return fmt.Errorf("list llm configs: %w", err)
	}
	for _, o := range all {
		if o.ID == c.ID || !o.IsDefault {
			continue
		}
		full, err := s.d.LLMConfig.GetLLMConfig(ctx, o.ID) // decrypted, so the key survives the update
		if err != nil {
			return fmt.Errorf("get llm config: %w", err)
		}
		full.IsDefault = false
		full.UpdatedAt = s.now()
		if err := s.d.LLMConfig.UpdateLLMConfig(ctx, full); err != nil {
			return fmt.Errorf("clear default llm config: %w", err)
		}
	}
	return nil
}

func (s *server) listLLMConfigs(w http.ResponseWriter, r *http.Request) {
	items, err := s.d.LLMConfig.ListLLMConfigs(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list llm configs: %w", err))
		return
	}
	out := make([]domain.LLMConfig, len(items))
	for i := range items {
		out[i] = *public(&items[i])
	}
	writeJSON(w, http.StatusOK, newList(out, len(out)))
}

func (s *server) getLLMConfig(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	c, err := s.loadLLMConfig(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": public(c)})
}

func (s *server) createLLMConfig(w http.ResponseWriter, r *http.Request) {
	var b llmBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	now := s.now()
	c := &domain.LLMConfig{ID: uuid.New(), OrgID: orgID, CreatedAt: now, UpdatedAt: now}
	if err := s.applyLLMBody(ctx, b, c, true); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !c.IsDefault {
		// The first config of an organisation becomes its default.
		existing, err := s.d.LLMConfig.ListLLMConfigs(ctx, orgID)
		if err != nil {
			s.writeErr(w, r, fmt.Errorf("list llm configs: %w", err))
			return
		}
		c.IsDefault = len(existing) == 0
	}
	if c.IsDefault {
		if err := s.makeSoleDefault(ctx, c); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if err := s.d.LLMConfig.CreateLLMConfig(ctx, c); err != nil {
		s.writeErr(w, r, fmt.Errorf("create llm config: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"config": public(c)})
}

func (s *server) updateLLMConfig(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var b llmBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.loadLLMConfig(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.applyLLMBody(ctx, b, c, false); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if c.IsDefault {
		if err := s.makeSoleDefault(ctx, c); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	c.UpdatedAt = s.now()
	if err := s.d.LLMConfig.UpdateLLMConfig(ctx, c); err != nil {
		s.writeErr(w, r, fmt.Errorf("update llm config: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": public(c)})
}

func (s *server) deleteLLMConfig(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	orgID := claimsOf(r).OrgID
	if _, err := s.loadLLMConfig(ctx, orgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	// Unlink fallback references so no chain points at a deleted config.
	all, err := s.d.LLMConfig.ListLLMConfigs(ctx, orgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list llm configs: %w", err))
		return
	}
	for _, o := range all {
		if o.FallbackID == nil || *o.FallbackID != id || o.ID == id {
			continue
		}
		full, err := s.d.LLMConfig.GetLLMConfig(ctx, o.ID)
		if err != nil {
			s.writeErr(w, r, fmt.Errorf("get llm config: %w", err))
			return
		}
		full.FallbackID = nil
		full.UpdatedAt = s.now()
		if err := s.d.LLMConfig.UpdateLLMConfig(ctx, full); err != nil {
			s.writeErr(w, r, fmt.Errorf("unlink fallback: %w", err))
			return
		}
	}
	if err := s.d.LLMConfig.DeleteLLMConfig(ctx, id); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			s.writeErr(w, r, errConflict("LLM config is still used by an agent profile"))
			return
		}
		s.writeErr(w, r, fmt.Errorf("delete llm config: %w", err))
		return
	}
	noContent(w)
}

type llmTestResponse struct {
	OK        bool   `json:"ok"`
	Reply     string `json:"reply"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

func (s *server) testLLMConfig(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	c, err := s.loadLLMConfig(r.Context(), claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if s.d.LLMTester == nil {
		writeJSON(w, http.StatusOK, llmTestResponse{OK: false, Error: "LLM tester not configured"})
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = defaultTestPrompt
	}
	ctx, cancel := context.WithTimeout(r.Context(), llmTestTimeout)
	defer cancel()
	start := time.Now()
	reply, latency, err := s.d.LLMTester.Test(ctx, *c, prompt)
	if latency <= 0 {
		latency = time.Since(start)
	}
	res := llmTestResponse{OK: err == nil, Reply: reply, LatencyMs: latency.Milliseconds()}
	if err != nil {
		res.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}
