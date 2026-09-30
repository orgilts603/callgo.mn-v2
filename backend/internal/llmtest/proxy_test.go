package llmtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func cfg(provider domain.LLMProvider, baseURL string) domain.LLMConfig {
	return domain.LLMConfig{
		Name: "t", Provider: provider, Model: "m1", BaseURL: baseURL,
		APIKey: "sk-secret-123", Temperature: 0.3, MaxTokens: 100,
	}
}

func TestViaWorker(t *testing.T) {
	var gotPath string
	var got map[string]any
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		assert.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "reply": "hello", "latencyMs": 123})
	}))
	defer worker.Close()

	reply, lat, err := NewProxy(worker.URL+"/", nil).Test(context.Background(), cfg(domain.ProviderAnthropic, ""), "hi")
	require.NoError(t, err)
	assert.Equal(t, "hello", reply)
	assert.Equal(t, 123*time.Millisecond, lat)
	assert.Equal(t, "/test-llm", gotPath)
	assert.Equal(t, "hi", got["prompt"])
	c, ok := got["config"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sk-secret-123", c["apiKey"], "api key is included for the worker")
	assert.Equal(t, "anthropic", c["provider"])
	assert.Equal(t, "m1", c["model"])
}

func TestWorkerReportsFailure(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid api key sk-secret-123", "latencyMs": 5})
	}))
	defer worker.Close()

	// Must not fall back to direct even for an openai-compatible provider.
	direct := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("direct endpoint must not be called when the worker answered")
	}))
	defer direct.Close()

	_, _, err := NewProxy(worker.URL, nil).Test(context.Background(), cfg(domain.ProviderOpenAICompatible, direct.URL), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTestFailed)
	assert.Contains(t, err.Error(), "invalid api key")
	assert.NotContains(t, err.Error(), "sk-secret-123", "api key is redacted")
}

func TestDirectFallbackWhenWorkerDown(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":" pong "}}]}`))
	}))
	defer llm.Close()

	deadWorker := httptest.NewServer(nil)
	deadURL := deadWorker.URL
	deadWorker.Close()

	for _, provider := range []domain.LLMProvider{domain.ProviderOpenAICompatible, domain.ProviderOpenAI, domain.ProviderGroq, domain.ProviderOllama} {
		gotBody, gotAuth, gotPath = nil, "", ""
		reply, lat, err := NewProxy(deadURL, nil).Test(context.Background(), cfg(provider, llm.URL+"/v1/"), "ping")
		require.NoError(t, err, provider)
		assert.Equal(t, "pong", reply)
		assert.Positive(t, lat)
		assert.Equal(t, "/v1/chat/completions", gotPath)
		assert.Equal(t, "Bearer sk-secret-123", gotAuth)
		assert.Equal(t, "m1", gotBody["model"])
		msgs, _ := gotBody["messages"].([]any)
		require.Len(t, msgs, 1)
		assert.Equal(t, "ping", msgs[0].(map[string]any)["content"])
	}
}

func TestDirectFallbackOnWorker503(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer worker.Close()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer llm.Close()

	reply, _, err := NewProxy(worker.URL, nil).Test(context.Background(), cfg(domain.ProviderGroq, llm.URL), "x")
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

func TestDirectProviderError(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-secret-123"}}`))
	}))
	defer llm.Close()

	_, _, err := NewProxy("http://127.0.0.1:1", nil).Test(context.Background(), cfg(domain.ProviderOpenAI, llm.URL), "x")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTestFailed)
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.Contains(t, err.Error(), "Incorrect API key")
	assert.NotContains(t, err.Error(), "sk-secret-123")
}

func TestDirectValidation(t *testing.T) {
	p := NewProxy("http://127.0.0.1:1", nil)

	t.Run("compatible needs base url", func(t *testing.T) {
		_, _, err := p.Test(context.Background(), cfg(domain.ProviderOpenAICompatible, ""), "x")
		require.ErrorIs(t, err, ErrTestFailed)
		assert.Contains(t, err.Error(), "base URL")
	})
	t.Run("openai needs key", func(t *testing.T) {
		c := cfg(domain.ProviderOpenAI, "http://127.0.0.1:1")
		c.APIKey = ""
		_, _, err := p.Test(context.Background(), c, "x")
		require.ErrorIs(t, err, ErrTestFailed)
		assert.Contains(t, err.Error(), "API key")
	})
	t.Run("empty choices", func(t *testing.T) {
		llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}))
		defer llm.Close()
		_, _, err := p.Test(context.Background(), cfg(domain.ProviderOpenAI, llm.URL), "x")
		require.ErrorIs(t, err, ErrTestFailed)
	})
}

func TestNoFallbackForNativeProviders(t *testing.T) {
	for _, provider := range []domain.LLMProvider{domain.ProviderAnthropic, domain.ProviderGoogle} {
		_, _, err := NewProxy("http://127.0.0.1:1", nil).Test(context.Background(), cfg(provider, ""), "x")
		require.Error(t, err, provider)
		assert.ErrorIs(t, err, ErrWorkerUnreachable)
		assert.Contains(t, err.Error(), string(provider))
		assert.NotContains(t, err.Error(), "sk-secret-123")
	}
}

func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("must not fall back when the context is done")
	}))
	defer llm.Close()
	_, _, err := NewProxy("http://127.0.0.1:1", nil).Test(ctx, cfg(domain.ProviderOpenAI, llm.URL), "x")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestDefaultPrompt(t *testing.T) {
	var got map[string]any
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"reply":"r"}`))
	}))
	defer worker.Close()
	_, lat, err := NewProxy(worker.URL, nil).Test(context.Background(), cfg(domain.ProviderGoogle, ""), "  ")
	require.NoError(t, err)
	assert.Equal(t, DefaultPrompt, got["prompt"])
	assert.Positive(t, lat, "falls back to measured latency")
}
