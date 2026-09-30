package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var fastRetry = retryPolicy{attempts: 3, base: time.Millisecond, max: 5 * time.Millisecond}

// fakeVec derives a deterministic vector of n dims from text.
func fakeVec(text string, n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = float32(len(text)+i) / 100
	}
	return v
}

func TestOpenAIShape(t *testing.T) {
	var calls atomic.Int32
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer sk-secret", r.Header.Get("Authorization"))
		var req openAIRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "text-embedding-3-small", req.Model)
		sizes = append(sizes, len(req.Input))
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		// Answer out of order to exercise index handling.
		data := make([]item, len(req.Input))
		for i := range req.Input {
			j := len(req.Input) - 1 - i
			assert.NotEmpty(t, req.Input[j])
			data[i] = item{Index: j, Embedding: fakeVec(req.Input[j], 4)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	e, err := New(domain.ProviderOpenAI, "", "sk-secret", srv.URL+"/v1/", srv.Client())
	require.NoError(t, err)
	require.Equal(t, DefaultOpenAIModel, e.Model())
	require.Equal(t, 1536, e.Dims(), "known model dims before any call")

	texts := make([]string, 130)
	for i := range texts {
		texts[i] = strings.Repeat("x", i)
	}
	vecs, err := e.Embed(context.Background(), texts)
	require.EqualError(t, err, "embed: provider returned 4 dimensions, expected 1536")
	require.Nil(t, vecs)

	// Unknown model: dims learnt from the first response.
	e, err = New(domain.ProviderOpenAICompatible, "my-embed", "sk-secret", srv.URL+"/v1", srv.Client())
	require.NoError(t, err)
	e.(*OpenAI).model = "text-embedding-3-small" // server asserts the model name
	e.(*OpenAI).dims.Store(0)
	sizes = nil
	vecs, err = e.Embed(context.Background(), texts)
	require.NoError(t, err)
	require.Len(t, vecs, 130)
	require.Equal(t, []int{64, 64, 2}, sizes, "batched by 64")
	for i, v := range vecs {
		want := texts[i]
		if want == "" {
			want = " "
		}
		require.Equal(t, fakeVec(want, 4), v, "vector %d in input order", i)
	}
	require.Equal(t, 4, e.Dims())

	v, err := Query(context.Background(), e, "hello")
	require.NoError(t, err)
	require.Equal(t, fakeVec("hello", 4), v)

	empty, err := e.Embed(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestOllamaDefaultsAndNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		var req openAIRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, DefaultOllamaModel, req.Model)
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2,0.3]}]}`)
	}))
	defer srv.Close()
	e, err := New(domain.ProviderOllama, "", "", srv.URL, nil)
	require.NoError(t, err)
	require.Equal(t, DefaultOllamaModel, e.Model())
	e.(*OpenAI).dims.Store(0)
	vecs, err := e.Embed(context.Background(), []string{"сайн уу"})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{0.1, 0.2, 0.3}}, vecs)
	require.Equal(t, 3, e.Dims())

	o, err := New(domain.ProviderOllama, "", "", "", nil)
	require.NoError(t, err)
	require.Equal(t, DefaultOllamaBaseURL, o.(*OpenAI).baseURL)
}

func TestGoogleShape(t *testing.T) {
	var tasks []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1beta/models/text-embedding-004:batchEmbedContents", r.URL.Path)
		assert.Empty(t, r.URL.Query().Get("key"), "key not in the URL")
		assert.Equal(t, "g-secret", r.Header.Get("x-goog-api-key"))
		var req googleBatchRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		type emb struct {
			Values []float32 `json:"values"`
		}
		out := make([]emb, len(req.Requests))
		for i, rq := range req.Requests {
			assert.Equal(t, "models/text-embedding-004", rq.Model)
			tasks = append(tasks, rq.TaskType)
			out[i] = emb{Values: fakeVec(rq.Content.Parts[0].Text, 768)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	defer srv.Close()

	e, err := New(domain.ProviderGoogle, "", "g-secret", srv.URL+"/v1beta", srv.Client())
	require.NoError(t, err)
	require.Equal(t, DefaultGoogleModel, e.Model())
	require.Equal(t, 768, e.Dims())
	vecs, err := e.Embed(context.Background(), make([]string, 70))
	require.NoError(t, err)
	require.Len(t, vecs, 70)
	require.Len(t, vecs[0], 768)
	for _, task := range tasks {
		require.Equal(t, "RETRIEVAL_DOCUMENT", task)
	}
	tasks = nil
	q, err := Query(context.Background(), e, "асуулт")
	require.NoError(t, err)
	require.Equal(t, fakeVec("асуулт", 768), q)
	require.Equal(t, []string{"RETRIEVAL_QUERY"}, tasks)
}

func TestRetriesAndErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
		}
	}))
	defer srv.Close()
	e, err := New(domain.ProviderOpenAICompatible, "m", "k", srv.URL, srv.Client())
	require.NoError(t, err)
	e.(*OpenAI).retry = fastRetry
	vecs, err := e.Embed(context.Background(), []string{"a"})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{1, 0}}, vecs)
	require.EqualValues(t, 3, calls.Load())

	// Persistent 5xx gives up after the configured attempts.
	calls.Store(0)
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"overloaded"}`)
	}))
	defer always.Close()
	e, err = New(domain.ProviderOpenAICompatible, "m", "", always.URL, always.Client())
	require.NoError(t, err)
	e.(*OpenAI).retry = fastRetry
	_, err = e.Embed(context.Background(), []string{"a"})
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
	require.Equal(t, "overloaded", apiErr.Message)
	require.EqualValues(t, 3, calls.Load())

	// 401 is not retried and the key never leaks into the error.
	calls.Store(0)
	unauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided: sk-live-123456"}}`)
	}))
	defer unauth.Close()
	e, err = New(domain.ProviderOpenAI, "", "sk-live-123456", unauth.URL, unauth.Client())
	require.NoError(t, err)
	e.(*OpenAI).retry = fastRetry
	_, err = e.Embed(context.Background(), []string{"a"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sk-live-123456")
	require.Contains(t, err.Error(), "HTTP 401")
	require.EqualValues(t, 1, calls.Load())

	// Cancelled context stops retrying.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e, err = New(domain.ProviderOpenAICompatible, "m", "", always.URL, always.Client())
	require.NoError(t, err)
	_, err = e.Embed(ctx, []string{"a"})
	require.True(t, errors.Is(err, context.Canceled), "got %v", err)

	// Transport errors are retried then reported without the key.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	e, err = New(domain.ProviderGoogle, "", "g-key-789", deadURL, nil)
	assert.NoError(t, err)
	e.(*Google).retry = fastRetry
	_, err = e.Embed(context.Background(), []string{"a"})
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "g-key-789")

	// Mismatched vector count.
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer short.Close()
	e, err = New(domain.ProviderOpenAICompatible, "m", "", short.URL, nil)
	require.NoError(t, err)
	_, err = e.Embed(context.Background(), []string{"a"})
	require.ErrorContains(t, err, "0 vectors for 1 texts")
}

func TestNewValidation(t *testing.T) {
	for _, tc := range []struct {
		p              domain.LLMProvider
		model, key, bu string
	}{
		{domain.ProviderOpenAI, "", "", ""},
		{domain.ProviderGoogle, "", "", ""},
		{domain.ProviderOpenAICompatible, "m", "k", ""},
		{domain.ProviderOpenAICompatible, "", "k", "http://x"},
		{domain.ProviderAnthropic, "", "k", ""},
		{domain.ProviderGroq, "", "k", ""},
	} {
		_, err := New(tc.p, tc.model, tc.key, tc.bu, nil)
		require.Truef(t, errors.Is(err, domain.ErrInvalid), "%s: %v", tc.p, err)
	}
	require.True(t, Supports(domain.ProviderOllama))
	require.False(t, Supports(domain.ProviderAnthropic))
	require.Equal(t, "", DefaultModel(domain.ProviderOpenAICompatible))
	h, err := New("hash", "", "", "", nil)
	require.NoError(t, err)
	require.Equal(t, HashModel, h.Model())
}

func TestHashDeterministic(t *testing.T) {
	h := NewHash(0)
	require.Equal(t, DefaultHashDims, h.Dims())
	require.Equal(t, "hash", h.Model())
	ctx := context.Background()
	texts := []string{"Хүргэлт 24 цагийн дотор үнэгүй", "Төлбөрийг QPay-ээр төлнө", ""}
	a, err := h.Embed(ctx, texts)
	require.NoError(t, err)
	b, err := NewHash(256).Embed(ctx, texts)
	require.NoError(t, err)
	require.Equal(t, a, b, "deterministic across instances")
	for _, v := range a[:2] {
		require.Len(t, v, 256)
		var n float64
		for _, x := range v {
			n += float64(x) * float64(x)
		}
		require.InDelta(t, 1.0, math.Sqrt(n), 1e-5, "L2-normalised")
	}
	for _, x := range a[2] {
		require.Zero(t, x, "empty text → zero vector")
	}

	cos := func(x, y []float32) float64 {
		var d float64
		for i := range x {
			d += float64(x[i]) * float64(y[i])
		}
		return d
	}
	q, err := Query(ctx, h, "хүргэлт хэдэн цагт")
	require.NoError(t, err)
	require.Greater(t, cos(q, a[0]), cos(q, a[1]), "lexical overlap ranks higher")
	infl, err := h.EmbedQuery(ctx, "хүргэлтийн")
	require.NoError(t, err)
	require.Greater(t, cos(infl, a[0]), 0.0, "prefix feature links inflected forms")

	ctxDone, cancel := context.WithCancel(ctx)
	cancel()
	_, err = h.Embed(ctxDone, texts)
	require.Error(t, err)
	require.Len(t, NewHash(8).vector("a b c d e f g"), 8)
}
