package embed

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// HashModel is the Model() of the feature-hashing embedder; a knowledge base
// whose EmbeddingModel is HashModel is embedded offline.
const HashModel = "hash"

// DefaultHashDims is the vector size NewHash uses for dims <= 0.
const DefaultHashDims = 256

// Hash is a deterministic, offline embedder: word unigrams and bigrams (plus
// a short prefix of long words, a crude stem for inflected languages such as
// Mongolian) are hashed with FNV-1a into dims signed buckets and the vector
// is L2-normalised. It needs no network or API key and suits development and
// tests; retrieval quality is lexical, not semantic.
type Hash struct {
	dims int
}

// NewHash returns a hashing embedder of dims dimensions (256 when dims <= 0).
func NewHash(dims int) *Hash {
	if dims <= 0 {
		dims = DefaultHashDims
	}
	return &Hash{dims: dims}
}

// Model returns "hash".
func (h *Hash) Model() string { return HashModel }

// Dims returns the vector size.
func (h *Hash) Dims() int { return h.dims }

// Embed hashes every text; it never fails except on a cancelled context.
func (h *Hash) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = h.vector(t)
	}
	return out, nil
}

// EmbedQuery hashes a query exactly like a document.
func (h *Hash) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return h.vector(text), nil
}

const hashStemRunes = 5

func (h *Hash) vector(text string) []float32 {
	vec := make([]float64, h.dims)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	add := func(feature string, weight float64) {
		f := fnv.New64a()
		_, _ = f.Write([]byte(feature))
		sum := f.Sum64()
		idx := int(sum % uint64(h.dims))
		if sum>>63 == 1 {
			weight = -weight
		}
		vec[idx] += weight
	}
	for i, w := range words {
		add("u:"+w, 1)
		if r := []rune(w); len(r) > hashStemRunes+1 {
			add("p:"+string(r[:hashStemRunes]), 0.5)
		}
		if i > 0 {
			add("b:"+words[i-1]+" "+w, 0.5)
		}
	}
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	out := make([]float32, h.dims)
	if norm == 0 {
		return out
	}
	norm = math.Sqrt(norm)
	for i, v := range vec {
		out[i] = float32(v / norm)
	}
	return out
}
