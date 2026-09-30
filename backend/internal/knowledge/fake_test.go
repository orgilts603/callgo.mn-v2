package knowledge

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// memRepo is an in-memory domain.KnowledgeRepository (+ TextStore,
// ChunkLister) for service tests.
type memRepo struct {
	mu     sync.Mutex
	bases  map[uuid.UUID]*domain.KnowledgeBase
	docs   map[uuid.UUID]*domain.KnowledgeDocument
	texts  map[uuid.UUID]string
	chunks map[uuid.UUID][]domain.KnowledgeChunk // by document
	order  []uuid.UUID                           // document creation order
}

func newMemRepo() *memRepo {
	return &memRepo{bases: map[uuid.UUID]*domain.KnowledgeBase{}, docs: map[uuid.UUID]*domain.KnowledgeDocument{},
		texts: map[uuid.UUID]string{}, chunks: map[uuid.UUID][]domain.KnowledgeChunk{}}
}

var (
	_ domain.KnowledgeRepository = (*memRepo)(nil)
	_ TextStore                  = (*memRepo)(nil)
	_ ChunkLister                = (*memRepo)(nil)
)

func (m *memRepo) counts(kb *domain.KnowledgeBase) domain.KnowledgeBase {
	c := *kb
	c.DocumentCount, c.ChunkCount = 0, 0
	for _, d := range m.docs {
		if d.KnowledgeBaseID == kb.ID {
			c.DocumentCount++
			c.ChunkCount += len(m.chunks[d.ID])
		}
	}
	return c
}

func (m *memRepo) CreateKnowledgeBase(_ context.Context, kb *domain.KnowledgeBase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kb.ID = uuid.New()
	kb.CreatedAt, kb.UpdatedAt = time.Now(), time.Now()
	c := *kb
	m.bases[kb.ID] = &c
	return nil
}

func (m *memRepo) UpdateKnowledgeBase(_ context.Context, kb *domain.KnowledgeBase) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.bases[kb.ID]
	if !ok {
		return domain.ErrNotFound
	}
	c := *kb
	c.EmbeddingDims, c.CreatedAt = cur.EmbeddingDims, cur.CreatedAt
	if c.EmbeddingModel != cur.EmbeddingModel {
		c.EmbeddingDims = 0
	}
	m.bases[kb.ID] = &c
	*kb = m.counts(&c)
	return nil
}

func (m *memRepo) DeleteKnowledgeBase(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bases[id]; !ok {
		return domain.ErrNotFound
	}
	delete(m.bases, id)
	for did, d := range m.docs {
		if d.KnowledgeBaseID == id {
			delete(m.docs, did)
			delete(m.chunks, did)
		}
	}
	return nil
}

func (m *memRepo) GetKnowledgeBase(_ context.Context, id uuid.UUID) (*domain.KnowledgeBase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kb, ok := m.bases[id]
	if !ok {
		return nil, fmt.Errorf("mem: %w", domain.ErrNotFound)
	}
	c := m.counts(kb)
	return &c, nil
}

func (m *memRepo) ListKnowledgeBases(_ context.Context, orgID uuid.UUID) ([]domain.KnowledgeBase, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KnowledgeBase
	for _, kb := range m.bases {
		if kb.OrgID == orgID {
			out = append(out, m.counts(kb))
		}
	}
	return out, nil
}

func (m *memRepo) CreateDocument(_ context.Context, d *domain.KnowledgeDocument) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.bases[d.KnowledgeBaseID]; !ok {
		return domain.ErrNotFound
	}
	d.ID = uuid.New()
	d.CreatedAt, d.UpdatedAt = time.Now(), time.Now()
	c := *d
	m.docs[d.ID] = &c
	m.order = append(m.order, d.ID)
	return nil
}

func (m *memRepo) UpdateDocument(_ context.Context, d *domain.KnowledgeDocument) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.docs[d.ID]
	if !ok {
		return fmt.Errorf("mem: %w", domain.ErrNotFound)
	}
	c := *d
	c.KnowledgeBaseID, c.OrgID, c.CreatedAt = cur.KnowledgeBaseID, cur.OrgID, cur.CreatedAt
	m.docs[d.ID] = &c
	*d = c
	return nil
}

func (m *memRepo) DeleteDocument(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; !ok {
		return domain.ErrNotFound
	}
	delete(m.docs, id)
	delete(m.chunks, id)
	return nil
}

func (m *memRepo) GetDocument(_ context.Context, id uuid.UUID) (*domain.KnowledgeDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.docs[id]
	if !ok {
		return nil, fmt.Errorf("mem: %w", domain.ErrNotFound)
	}
	c := *d
	return &c, nil
}

func (m *memRepo) ListDocuments(_ context.Context, kbID uuid.UUID) ([]domain.KnowledgeDocument, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KnowledgeDocument
	for _, id := range m.order {
		if d, ok := m.docs[id]; ok && d.KnowledgeBaseID == kbID {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (m *memRepo) SetDocumentText(_ context.Context, id uuid.UUID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; !ok {
		return domain.ErrNotFound
	}
	m.texts[id] = text
	return nil
}

func (m *memRepo) DocumentText(_ context.Context, id uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; !ok {
		return "", domain.ErrNotFound
	}
	return m.texts[id], nil
}

func (m *memRepo) ReplaceChunks(_ context.Context, docID uuid.UUID, chunks []domain.KnowledgeChunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.docs[docID]
	if !ok {
		return fmt.Errorf("mem: %w", domain.ErrNotFound)
	}
	kb := m.bases[d.KnowledgeBaseID]
	dims := 0
	if len(chunks) > 0 && chunks[0].Embedding != nil {
		dims = len(chunks[0].Embedding)
	}
	if dims > 0 {
		if kb.EmbeddingDims != 0 && kb.EmbeddingDims != dims {
			return fmt.Errorf("mem: %w: dims", domain.ErrInvalid)
		}
		kb.EmbeddingDims = dims
	}
	cp := make([]domain.KnowledgeChunk, len(chunks))
	for i, c := range chunks {
		c.ID = uuid.New()
		c.DocumentID, c.KnowledgeBaseID = docID, kb.ID
		cp[i] = c
	}
	m.chunks[docID] = cp
	return nil
}

func (m *memRepo) ListChunks(_ context.Context, docID uuid.UUID, limit, offset int) ([]domain.KnowledgeChunk, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := m.chunks[docID]
	if offset > len(all) {
		offset = len(all)
	}
	end := min(offset+limit, len(all))
	return append([]domain.KnowledgeChunk(nil), all[offset:end]...), len(all), nil
}

func (m *memRepo) baseChunks(kbID uuid.UUID) []domain.KnowledgeChunk {
	var out []domain.KnowledgeChunk
	for _, id := range m.order {
		if d, ok := m.docs[id]; ok && d.KnowledgeBaseID == kbID {
			out = append(out, m.chunks[id]...)
		}
	}
	return out
}

func (m *memRepo) hit(c domain.KnowledgeChunk, score float64) domain.KnowledgeHit {
	return domain.KnowledgeHit{ChunkID: c.ID, DocumentID: c.DocumentID, Filename: m.docs[c.DocumentID].Filename,
		Heading: c.Heading, Content: c.Content, Score: float32(score)}
}

func (m *memRepo) SearchVector(_ context.Context, kbID uuid.UUID, q []float32, k int) ([]domain.KnowledgeHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var hits []domain.KnowledgeHit
	for _, c := range m.baseChunks(kbID) {
		if len(c.Embedding) != len(q) {
			continue
		}
		var dot, na, nb float64
		for i := range q {
			dot += float64(q[i]) * float64(c.Embedding[i])
			na += float64(q[i]) * float64(q[i])
			nb += float64(c.Embedding[i]) * float64(c.Embedding[i])
		}
		score := 0.0
		if na > 0 && nb > 0 {
			score = dot / math.Sqrt(na*nb)
		}
		hits = append(hits, m.hit(c, score))
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func (m *memRepo) SearchText(_ context.Context, kbID uuid.UUID, query string, k int) ([]domain.KnowledgeHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	qw := words(query)
	var hits []domain.KnowledgeHit
	for _, c := range m.baseChunks(kbID) {
		n := 0
		cw := words(c.Heading + " " + c.Content)
		for _, q := range qw {
			for _, w := range cw {
				if w == q {
					n++
				}
			}
		}
		if n > 0 {
			hits = append(hits, m.hit(c, float64(n)))
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func (m *memRepo) AllChunksText(_ context.Context, kbID uuid.UUID, maxChars int) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, c := range m.baseChunks(kbID) {
		piece := "## " + c.Heading + "\n" + c.Content + "\n\n"
		if len([]rune(b.String()+piece)) > maxChars {
			return b.String(), true, nil
		}
		b.WriteString(piece)
	}
	return b.String(), false, nil
}

// memConfigs is an in-memory domain.LLMConfigRepository.
type memConfigs struct {
	mu   sync.Mutex
	list []domain.LLMConfig
}

func (c *memConfigs) add(cfg domain.LLMConfig) *domain.LLMConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg.ID = uuid.New()
	c.list = append(c.list, cfg)
	return &cfg
}

func (c *memConfigs) CreateLLMConfig(context.Context, *domain.LLMConfig) error { return nil }
func (c *memConfigs) UpdateLLMConfig(context.Context, *domain.LLMConfig) error { return nil }
func (c *memConfigs) DeleteLLMConfig(context.Context, uuid.UUID) error         { return nil }

func (c *memConfigs) GetLLMConfig(_ context.Context, id uuid.UUID) (*domain.LLMConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cfg := range c.list {
		if cfg.ID == id {
			cp := cfg
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (c *memConfigs) GetDefaultLLMConfig(_ context.Context, orgID uuid.UUID) (*domain.LLMConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cfg := range c.list {
		if cfg.OrgID == orgID && cfg.IsDefault {
			cp := cfg
			return &cp, nil
		}
	}
	for _, cfg := range c.list {
		if cfg.OrgID == orgID {
			cp := cfg
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (c *memConfigs) ListLLMConfigs(context.Context, uuid.UUID) ([]domain.LLMConfig, error) {
	return c.list, nil
}

// stubEmbedder returns fixed-size vectors derived from word features, or
// errors on demand.
type stubEmbedder struct {
	dims     int
	fail     error
	failQ    error
	block    chan struct{}
	mu       sync.Mutex
	calls    int
	inputs   int
	queryHit int
}

func (e *stubEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e.mu.Lock()
	e.calls++
	e.inputs += len(texts)
	e.mu.Unlock()
	if e.fail != nil {
		return nil, e.fail
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, e.dims)
		for _, w := range words(t) {
			v[len(w)%e.dims]++
		}
		out[i] = v
	}
	return out, nil
}

func (e *stubEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	e.queryHit++
	e.mu.Unlock()
	if e.failQ != nil {
		return nil, e.failQ
	}
	v, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

func (e *stubEmbedder) Dims() int     { return e.dims }
func (e *stubEmbedder) Model() string { return "stub" }
