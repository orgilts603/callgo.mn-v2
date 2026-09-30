// Package embed turns texts into vectors for the knowledge base (RAG). It
// implements domain.Embedder for OpenAI, OpenAI-compatible servers (incl.
// Ollama's /v1 API) and Google Gemini, plus an offline feature-hashing
// embedder for development and tests.
package embed

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Provider-specific defaults.
const (
	DefaultOpenAIModel = "text-embedding-3-small"
	DefaultGoogleModel = "text-embedding-004"
	DefaultOllamaModel = "nomic-embed-text"

	DefaultOpenAIBaseURL = "https://api.openai.com/v1"
	DefaultOllamaBaseURL = "http://localhost:11434/v1"
	DefaultGoogleBaseURL = "https://generativelanguage.googleapis.com/v1beta"

	// MaxBatch is the number of texts sent per HTTP request.
	MaxBatch = 64

	defaultTimeout = 60 * time.Second
)

// knownDims lists the output size of common models so Dims() is right
// before the first call; other models are discovered from the first response.
var knownDims = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
	"text-embedding-ada-002": 1536,
	"text-embedding-004":     768,
	"text-embedding-005":     768,
	"gemini-embedding-001":   3072,
	"nomic-embed-text":       768,
	"mxbai-embed-large":      1024,
	"all-minilm":             384,
	"bge-m3":                 1024,
}

// QueryEmbedder is implemented by embedders that embed search queries
// differently from documents (Google's RETRIEVAL_QUERY task type).
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Query embeds a search query, using e's EmbedQuery when it has one.
func Query(ctx context.Context, e domain.Embedder, text string) ([]float32, error) {
	if q, ok := e.(QueryEmbedder); ok {
		return q.EmbedQuery(ctx, text)
	}
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embed: query: got %d vectors, want 1", len(vecs))
	}
	return vecs[0], nil
}

// DefaultModel returns the default embedding model of a provider, or "" when
// the provider has none (openai_compatible needs an explicit model;
// anthropic and groq offer no embeddings API).
func DefaultModel(p domain.LLMProvider) string {
	switch p {
	case domain.ProviderOpenAI:
		return DefaultOpenAIModel
	case domain.ProviderGoogle:
		return DefaultGoogleModel
	case domain.ProviderOllama:
		return DefaultOllamaModel
	}
	return ""
}

// Supports reports whether New can build an embedder for provider p.
func Supports(p domain.LLMProvider) bool {
	switch p {
	case domain.ProviderOpenAI, domain.ProviderOpenAICompatible, domain.ProviderOllama, domain.ProviderGoogle:
		return true
	}
	return false
}

// New builds an embedder. model and baseURL fall back to provider defaults
// (baseURL overrides the API root for every provider, which also serves
// proxies and tests). A nil client gets a 60s-timeout default. Errors wrap
// domain.ErrInvalid (unsupported provider, missing key/model/URL).
func New(provider domain.LLMProvider, model, apiKey, baseURL string, client *http.Client) (domain.Embedder, error) {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel(provider)
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)

	switch provider {
	case domain.ProviderOpenAI, domain.ProviderOpenAICompatible, domain.ProviderOllama:
		switch provider {
		case domain.ProviderOpenAI:
			if apiKey == "" {
				return nil, fmt.Errorf("%w: embed: openai needs an API key", domain.ErrInvalid)
			}
			if baseURL == "" {
				baseURL = DefaultOpenAIBaseURL
			}
		case domain.ProviderOllama:
			if baseURL == "" {
				baseURL = DefaultOllamaBaseURL
			}
		default:
			if baseURL == "" {
				return nil, fmt.Errorf("%w: embed: openai_compatible needs a base URL", domain.ErrInvalid)
			}
		}
		if model == "" {
			return nil, fmt.Errorf("%w: embed: %s needs an embedding model", domain.ErrInvalid, provider)
		}
		return newOpenAI(baseURL, model, apiKey, client), nil
	case domain.ProviderGoogle:
		if apiKey == "" {
			return nil, fmt.Errorf("%w: embed: google needs an API key", domain.ErrInvalid)
		}
		if baseURL == "" {
			baseURL = DefaultGoogleBaseURL
		}
		return newGoogle(baseURL, model, apiKey, client), nil
	case "hash":
		return NewHash(0), nil
	}
	return nil, fmt.Errorf("%w: embed: provider %q has no embeddings API", domain.ErrInvalid, provider)
}

// batches splits texts into slices of at most n.
func batches(texts []string, n int) [][]string {
	var out [][]string
	for len(texts) > n {
		out = append(out, texts[:n])
		texts = texts[n:]
	}
	if len(texts) > 0 {
		out = append(out, texts)
	}
	return out
}

// nonEmpty replaces blank inputs (rejected by some APIs) with a single space.
func nonEmpty(texts []string) []string {
	out := make([]string, len(texts))
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			t = " "
		}
		out[i] = t
	}
	return out
}
