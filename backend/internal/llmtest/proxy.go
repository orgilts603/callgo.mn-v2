// Package llmtest checks that an LLMConfig actually works. It asks the Python
// agent worker to run a tiny completion through the same plugin stack used in
// calls (POST <worker>/test-llm) and, when the worker is unreachable, falls
// back to a direct OpenAI-compatible chat completion for providers that speak
// that protocol.
package llmtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	// DefaultPrompt is used when the caller passes an empty prompt.
	DefaultPrompt = "Reply with one short friendly sentence."

	defaultTimeout   = 30 * time.Second
	maxResponseBytes = 1 << 20
)

// Sentinel errors, usable with errors.Is.
var (
	// ErrWorkerUnreachable means the agent worker could not be contacted and no
	// direct fallback exists for the provider.
	ErrWorkerUnreachable = errors.New("agent worker unreachable")
	// ErrTestFailed means the model or provider rejected the test request
	// (bad key, unknown model, ...). Its message carries the upstream reason.
	ErrTestFailed = errors.New("llm test failed")
)

// Provider default base URLs for the direct OpenAI-compatible fallback.
var defaultBaseURLs = map[domain.LLMProvider]string{
	domain.ProviderOpenAI: "https://api.openai.com/v1",
	domain.ProviderGroq:   "https://api.groq.com/openai/v1",
	domain.ProviderOllama: "http://localhost:11434/v1",
}

// Proxy runs LLM connectivity tests.
type Proxy struct {
	workerURL string
	client    *http.Client
}

// NewProxy creates a Proxy that talks to the agent worker at agentWorkerURL
// (e.g. http://localhost:8090). A nil client gets a 30s timeout default.
func NewProxy(agentWorkerURL string, client *http.Client) *Proxy {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Proxy{workerURL: strings.TrimRight(agentWorkerURL, "/"), client: client}
}

// workerResponse is the JSON returned by the worker's /test-llm endpoint.
type workerResponse struct {
	OK        bool    `json:"ok"`
	Reply     string  `json:"reply"`
	LatencyMs float64 `json:"latencyMs"`
	Error     string  `json:"error"`
}

// Test sends prompt to the model described by cfg and returns its reply and
// the round-trip latency. cfg.APIKey must be the decrypted key.
func (p *Proxy) Test(ctx context.Context, cfg domain.LLMConfig, prompt string) (string, time.Duration, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = DefaultPrompt
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	reply, latency, workerErr := p.viaWorker(ctx, cfg, prompt)
	if workerErr == nil {
		return reply, latency, nil
	}
	if !errors.Is(workerErr, ErrWorkerUnreachable) {
		return "", latency, redact(workerErr, cfg.APIKey)
	}
	if ctx.Err() != nil {
		return "", 0, fmt.Errorf("llm test: %w", ctx.Err())
	}
	if !supportsDirect(cfg.Provider) {
		return "", 0, fmt.Errorf("provider %q can only be tested through the agent worker: %w",
			cfg.Provider, redact(workerErr, cfg.APIKey))
	}
	reply, latency, err := p.direct(ctx, cfg, prompt)
	if err != nil {
		return "", latency, redact(err, cfg.APIKey)
	}
	return reply, latency, nil
}

func supportsDirect(p domain.LLMProvider) bool {
	switch p {
	case domain.ProviderOpenAI, domain.ProviderOpenAICompatible, domain.ProviderGroq, domain.ProviderOllama:
		return true
	}
	return false
}

// viaWorker calls the agent worker. Transport failures and 502/503/504 are
// reported as ErrWorkerUnreachable so the caller can fall back.
func (p *Proxy) viaWorker(ctx context.Context, cfg domain.LLMConfig, prompt string) (string, time.Duration, error) {
	cfgJSON, err := configWithKey(cfg)
	if err != nil {
		return "", 0, fmt.Errorf("encode llm config: %w", err)
	}
	body, err := json.Marshal(map[string]any{"config": cfgJSON, "prompt": prompt})
	if err != nil {
		return "", 0, fmt.Errorf("encode worker request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.workerURL+"/test-llm", bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("%w: build request: %v", ErrWorkerUnreachable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrWorkerUnreachable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	elapsed := time.Since(start)
	if err != nil {
		return "", elapsed, fmt.Errorf("%w: read response: %v", ErrWorkerUnreachable, err)
	}

	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "", elapsed, fmt.Errorf("%w: worker returned HTTP %d", ErrWorkerUnreachable, resp.StatusCode)
	}

	var out workerResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", elapsed, fmt.Errorf("agent worker returned HTTP %d with an unreadable body: %w",
			resp.StatusCode, err)
	}
	latency := time.Duration(out.LatencyMs * float64(time.Millisecond))
	if latency <= 0 {
		latency = elapsed
	}
	if !out.OK || resp.StatusCode >= 400 {
		msg := out.Error
		if msg == "" {
			msg = fmt.Sprintf("agent worker returned HTTP %d", resp.StatusCode)
		}
		return "", latency, fmt.Errorf("%w: %s", ErrTestFailed, msg)
	}
	return out.Reply, latency, nil
}

// configWithKey renders cfg as the worker expects: the domain JSON plus the
// decrypted "apiKey" (which domain.LLMConfig deliberately never marshals).
func configWithKey(cfg domain.LLMConfig) (map[string]any, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["apiKey"] = cfg.APIKey
	return m, nil
}

// direct performs a minimal OpenAI-compatible POST {base}/chat/completions.
func (p *Proxy) direct(ctx context.Context, cfg domain.LLMConfig, prompt string) (string, time.Duration, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBaseURLs[cfg.Provider]
	}
	if base == "" {
		return "", 0, fmt.Errorf("%w: base URL is required for provider %q", ErrTestFailed, cfg.Provider)
	}
	if cfg.Model == "" {
		return "", 0, fmt.Errorf("%w: model is required", ErrTestFailed)
	}
	if cfg.APIKey == "" && cfg.Provider != domain.ProviderOllama && cfg.Provider != domain.ProviderOpenAICompatible {
		return "", 0, fmt.Errorf("%w: API key is required for provider %q", ErrTestFailed, cfg.Provider)
	}

	body, err := json.Marshal(map[string]any{
		"model":    cfg.Model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", 0, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("%w: invalid base URL %q: %v", ErrTestFailed, base, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return "", time.Since(start), fmt.Errorf("%w: cannot reach %s: %v", ErrTestFailed, base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	latency := time.Since(start)
	if err != nil {
		return "", latency, fmt.Errorf("%w: read response: %v", ErrTestFailed, err)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	jsonErr := json.Unmarshal(raw, &parsed)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if jsonErr == nil && parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return "", latency, fmt.Errorf("%w: provider returned HTTP %d: %s", ErrTestFailed, resp.StatusCode, msg)
	}
	if jsonErr != nil {
		return "", latency, fmt.Errorf("%w: provider returned invalid JSON: %v", ErrTestFailed, jsonErr)
	}
	if len(parsed.Choices) == 0 {
		return "", latency, fmt.Errorf("%w: provider returned no choices", ErrTestFailed)
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), latency, nil
}

// redact removes secret from err's message so API keys never reach logs or
// API responses. The original error stays in the chain for errors.Is.
func redact(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return &redactedError{msg: strings.ReplaceAll(err.Error(), secret, "***"), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
