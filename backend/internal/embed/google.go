package embed

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Google task types.
const (
	taskDocument = "RETRIEVAL_DOCUMENT"
	taskQuery    = "RETRIEVAL_QUERY"
)

// Google embeds with the Gemini API batchEmbedContents endpoint. Documents
// use task type RETRIEVAL_DOCUMENT, queries (EmbedQuery) RETRIEVAL_QUERY.
type Google struct {
	httpClient
	baseURL string
	model   string
	apiKey  string
}

func newGoogle(baseURL, model, apiKey string, client *http.Client) *Google {
	model = strings.TrimPrefix(model, "models/")
	g := &Google{httpClient: httpClient{client: client, secret: apiKey, retry: defaultRetry},
		baseURL: baseURL, model: model, apiKey: apiKey}
	g.dims.Store(int64(knownDims[model]))
	return g
}

// Model returns the embedding model name.
func (g *Google) Model() string { return g.model }

// Dims returns the vector size (known per model or learnt from the first response).
func (g *Google) Dims() int { return int(g.dims.Load()) }

type googlePart struct {
	Text string `json:"text"`
}

type googleContent struct {
	Parts []googlePart `json:"parts"`
}

type googleEmbedRequest struct {
	Model    string        `json:"model"`
	Content  googleContent `json:"content"`
	TaskType string        `json:"taskType"`
}

type googleBatchRequest struct {
	Requests []googleEmbedRequest `json:"requests"`
}

type googleBatchResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// Embed embeds documents (RETRIEVAL_DOCUMENT).
func (g *Google) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return g.embed(ctx, texts, taskDocument)
}

// EmbedQuery embeds one search query (RETRIEVAL_QUERY).
func (g *Google) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := g.embed(ctx, []string{text}, taskQuery)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (g *Google) embed(ctx context.Context, texts []string, task string) ([][]float32, error) {
	// The key travels in the x-goog-api-key header rather than ?key= so it
	// never appears in URLs, logs or transport error messages.
	endpoint := g.baseURL + "/models/" + url.PathEscape(g.model) + ":batchEmbedContents"
	headers := map[string]string{"x-goog-api-key": g.apiKey}
	out := make([][]float32, 0, len(texts))
	for _, batch := range batches(nonEmpty(texts), MaxBatch) {
		req := googleBatchRequest{Requests: make([]googleEmbedRequest, len(batch))}
		for i, t := range batch {
			req.Requests[i] = googleEmbedRequest{Model: "models/" + g.model,
				Content: googleContent{Parts: []googlePart{{Text: t}}}, TaskType: task}
		}
		var resp googleBatchResponse
		if err := g.postJSON(ctx, endpoint, headers, req, &resp); err != nil {
			return nil, err
		}
		if len(resp.Embeddings) != len(batch) {
			return nil, fmt.Errorf("embed: provider returned %d vectors for %d texts", len(resp.Embeddings), len(batch))
		}
		vecs := make([][]float32, len(batch))
		for i, e := range resp.Embeddings {
			vecs[i] = e.Values
		}
		if err := g.noteDims(vecs); err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}
