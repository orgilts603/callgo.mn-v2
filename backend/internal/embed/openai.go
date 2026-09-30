package embed

import (
	"context"
	"fmt"
	"net/http"
)

// OpenAI embeds with the OpenAI /embeddings API shape (OpenAI,
// OpenAI-compatible servers, Ollama's /v1).
type OpenAI struct {
	httpClient
	baseURL string
	model   string
	apiKey  string
}

func newOpenAI(baseURL, model, apiKey string, client *http.Client) *OpenAI {
	o := &OpenAI{httpClient: httpClient{client: client, secret: apiKey, retry: defaultRetry},
		baseURL: baseURL, model: model, apiKey: apiKey}
	o.dims.Store(int64(knownDims[model]))
	return o
}

// Model returns the embedding model name.
func (o *OpenAI) Model() string { return o.model }

// Dims returns the vector size (known per model or learnt from the first
// response; 0 before that for unknown models).
func (o *OpenAI) Dims() int { return int(o.dims.Load()) }

type openAIRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per text, sending at most MaxBatch per request.
func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	headers := map[string]string{}
	if o.apiKey != "" {
		headers["Authorization"] = "Bearer " + o.apiKey
	}
	for _, batch := range batches(nonEmpty(texts), MaxBatch) {
		var resp openAIResponse
		if err := o.postJSON(ctx, o.baseURL+"/embeddings", headers,
			openAIRequest{Model: o.model, Input: batch}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Data) != len(batch) {
			return nil, fmt.Errorf("embed: provider returned %d vectors for %d texts", len(resp.Data), len(batch))
		}
		vecs := make([][]float32, len(batch))
		for i, d := range resp.Data {
			idx := d.Index
			if idx < 0 || idx >= len(batch) || vecs[idx] != nil {
				idx = i // servers that omit or repeat the index answer in order
			}
			vecs[idx] = d.Embedding
		}
		if err := o.noteDims(vecs); err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}
