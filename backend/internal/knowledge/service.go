// Package knowledge runs the knowledge-base (RAG) use cases: base
// management, background ingestion (extract → chunk → embed → store) and
// hybrid search (pgvector cosine + full-text, fused with Reciprocal Rank
// Fusion).
package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/embed"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/knowledge/chunk"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/knowledge/extract"
)

// Defaults and limits.
const (
	DefaultWorkers         = 2
	DefaultContextMaxChars = 60000
	DefaultQueueSize       = 256
	DefaultK               = 5
	MaxK                   = 50

	// HashDims is the dimension of the offline hash embedder used for
	// FakeEmbeddings.
	HashDims = 256

	minChunkSize = 200
	maxChunkSize = 8000

	// candidates is how many hits each retriever contributes to fusion.
	candidates = 20

	embedBatch    = 64
	jobTimeout    = 15 * time.Minute
	queryTimeout  = 15 * time.Second
	statusTimeout = 10 * time.Second
)

// Search modes.
const (
	ModeHybrid = "hybrid"
	ModeText   = "text"
)

// EmbedderFactory builds an embedder for an LLM config (APIKey decrypted)
// and embedding model.
type EmbedderFactory func(cfg *domain.LLMConfig, model string) (domain.Embedder, error)

// DefaultEmbedderFactory builds embedders with embed.New over client (nil →
// embed's default client).
func DefaultEmbedderFactory(client *http.Client) EmbedderFactory {
	return func(cfg *domain.LLMConfig, model string) (domain.Embedder, error) {
		return embed.New(cfg.Provider, model, cfg.APIKey, cfg.BaseURL, client)
	}
}

// TextStore keeps a document's extracted text so it can be re-chunked
// without the original upload (implemented by *crm.Store).
type TextStore interface {
	SetDocumentText(ctx context.Context, id uuid.UUID, text string) error
	DocumentText(ctx context.Context, id uuid.UUID) (string, error)
}

// ChunkLister pages a document's chunks (implemented by *crm.Store).
type ChunkLister interface {
	ListChunks(ctx context.Context, docID uuid.UUID, limit, offset int) ([]domain.KnowledgeChunk, int, error)
}

// Options tune the service.
type Options struct {
	// Workers is the number of concurrent ingestion jobs (default 2).
	Workers int
	// ContextMaxChars caps ContextText (default 60000).
	ContextMaxChars int
	// FakeEmbeddings embeds with the offline hash embedder (embed.NewHash)
	// when a base has no usable embedding config, instead of storing
	// text-only chunks. For development without API keys.
	FakeEmbeddings bool
	// QueueSize bounds pending ingestion jobs (default 256); AddDocument
	// blocks while the queue is full.
	QueueSize int
}

// Service implements the knowledge-base use cases. Create it with
// NewService and stop it with Close.
type Service struct {
	repo    domain.KnowledgeRepository
	texts   TextStore // nil when repo does not keep extracted text
	configs domain.LLMConfigRepository
	factory EmbedderFactory
	opts    Options
	log     zerolog.Logger

	jobs   chan job
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	active  map[uuid.UUID]bool // documents queued or being ingested
	pending map[uuid.UUID]job  // a newer job for an active document
	closed  bool
}

// job is one ingestion request. Exactly one source is set: raw bytes of an
// upload, pasted text, or neither (reuse the stored extracted text).
type job struct {
	docID    uuid.UUID
	raw      []byte
	text     *string
	filename string
	mime     string
}

// NewService starts the ingestion workers. embedderFactory may be nil
// (DefaultEmbedderFactory(nil)).
func NewService(repo domain.KnowledgeRepository, llmConfigs domain.LLMConfigRepository,
	embedderFactory func(cfg *domain.LLMConfig, model string) (domain.Embedder, error),
	opts Options, log zerolog.Logger) *Service {
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.ContextMaxChars <= 0 {
		opts.ContextMaxChars = DefaultContextMaxChars
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultQueueSize
	}
	factory := EmbedderFactory(embedderFactory)
	if factory == nil {
		factory = DefaultEmbedderFactory(nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		repo:    repo,
		configs: llmConfigs,
		factory: factory,
		opts:    opts,
		log:     log.With().Str("component", "knowledge").Logger(),
		jobs:    make(chan job, opts.QueueSize),
		ctx:     ctx,
		cancel:  cancel,
		active:  map[uuid.UUID]bool{},
		pending: map[uuid.UUID]job{},
	}
	if ts, ok := repo.(TextStore); ok {
		s.texts = ts
	}
	for i := 0; i < opts.Workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

// Close stops the workers, interrupting running jobs (their documents are
// marked failed and can be reprocessed), and waits for them to exit.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// ---------------------------------------------------------------------------
// Knowledge bases
// ---------------------------------------------------------------------------

// CreateBase validates and stores a base. Defaults: chunk 1200/200; the
// embedding config is kb.EmbeddingLLMConfigID or the org's default config
// (when its provider has an embeddings API); the model defaults per provider
// (openai_compatible requires one). Without a usable config the base
// is text-only (or uses the hash embedder with FakeEmbeddings).
func (s *Service) CreateBase(ctx context.Context, kb *domain.KnowledgeBase) error {
	if kb.OrgID == uuid.Nil {
		return fmt.Errorf("knowledge: %w: orgId is required", domain.ErrInvalid)
	}
	if err := normalizeBase(kb, nil); err != nil {
		return err
	}
	if err := s.resolveEmbedding(ctx, kb); err != nil {
		return err
	}
	kb.EmbeddingDims = 0
	if err := s.repo.CreateKnowledgeBase(ctx, kb); err != nil {
		return fmt.Errorf("knowledge: create base: %w", err)
	}
	return nil
}

// UpdateBase changes a base. Leaving both embedding fields empty keeps the
// current embedding settings. Changing them once the base holds embedded
// chunks is domain.ErrConflict. When the chunking parameters change, or the
// embedding settings of a base with text-only chunks, every document is
// re-ingested in the background.
func (s *Service) UpdateBase(ctx context.Context, kb *domain.KnowledgeBase) error {
	cur, err := s.repo.GetKnowledgeBase(ctx, kb.ID)
	if err != nil {
		return fmt.Errorf("knowledge: update base: %w", err)
	}
	kb.OrgID = cur.OrgID
	if err := normalizeBase(kb, cur); err != nil {
		return err
	}
	if kb.EmbeddingLLMConfigID == nil && strings.TrimSpace(kb.EmbeddingModel) == "" {
		kb.EmbeddingLLMConfigID, kb.EmbeddingModel = cur.EmbeddingLLMConfigID, cur.EmbeddingModel
	} else if err := s.resolveEmbedding(ctx, kb); err != nil {
		return err
	}
	embeddingChanged := kb.EmbeddingModel != cur.EmbeddingModel ||
		!sameID(kb.EmbeddingLLMConfigID, cur.EmbeddingLLMConfigID)
	if embeddingChanged && cur.ChunkCount > 0 && cur.EmbeddingDims > 0 {
		return fmt.Errorf("knowledge: %w: embedding settings are locked once the knowledge base has embedded chunks",
			domain.ErrConflict)
	}
	chunkingChanged := kb.ChunkSize != cur.ChunkSize || kb.ChunkOverlap != cur.ChunkOverlap
	if err := s.repo.UpdateKnowledgeBase(ctx, kb); err != nil {
		return fmt.Errorf("knowledge: update base: %w", err)
	}
	if cur.ChunkCount > 0 && (embeddingChanged || chunkingChanged) {
		s.reprocessAll(ctx, kb.ID)
	}
	return nil
}

// DeleteBase removes a base with its documents and chunks.
func (s *Service) DeleteBase(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.DeleteKnowledgeBase(ctx, id); err != nil {
		return fmt.Errorf("knowledge: delete base: %w", err)
	}
	return nil
}

// GetBase returns a base with its counts.
func (s *Service) GetBase(ctx context.Context, id uuid.UUID) (*domain.KnowledgeBase, error) {
	kb, err := s.repo.GetKnowledgeBase(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge: get base: %w", err)
	}
	return kb, nil
}

// ListBases lists an organisation's bases.
func (s *Service) ListBases(ctx context.Context, orgID uuid.UUID) ([]domain.KnowledgeBase, error) {
	list, err := s.repo.ListKnowledgeBases(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: list bases: %w", err)
	}
	return list, nil
}

// normalizeBase trims and validates the editable fields, filling chunking
// defaults (from cur when updating).
func normalizeBase(kb *domain.KnowledgeBase, cur *domain.KnowledgeBase) error {
	kb.Name = strings.TrimSpace(kb.Name)
	kb.Description = strings.TrimSpace(kb.Description)
	kb.EmbeddingModel = strings.TrimSpace(kb.EmbeddingModel)
	if kb.Name == "" {
		return fmt.Errorf("knowledge: %w: name is required", domain.ErrInvalid)
	}
	if utf8.RuneCountInString(kb.Name) > 200 {
		return fmt.Errorf("knowledge: %w: name is too long", domain.ErrInvalid)
	}
	defSize, defOverlap := chunk.DefaultSize, chunk.DefaultOverlap
	if cur != nil {
		defSize, defOverlap = cur.ChunkSize, cur.ChunkOverlap
	}
	if kb.ChunkSize == 0 {
		kb.ChunkSize = defSize
		if kb.ChunkOverlap == 0 {
			kb.ChunkOverlap = defOverlap
		}
	}
	if kb.ChunkSize < minChunkSize || kb.ChunkSize > maxChunkSize {
		return fmt.Errorf("knowledge: %w: chunkSize must be between %d and %d", domain.ErrInvalid,
			minChunkSize, maxChunkSize)
	}
	if kb.ChunkOverlap < 0 || kb.ChunkOverlap > kb.ChunkSize/2 {
		return fmt.Errorf("knowledge: %w: chunkOverlap must be between 0 and half the chunk size", domain.ErrInvalid)
	}
	return nil
}

// resolveEmbedding fills kb.EmbeddingLLMConfigID / EmbeddingModel.
func (s *Service) resolveEmbedding(ctx context.Context, kb *domain.KnowledgeBase) error {
	if kb.EmbeddingModel == embed.HashModel {
		kb.EmbeddingLLMConfigID = nil
		return nil
	}
	var cfg *domain.LLMConfig
	if kb.EmbeddingLLMConfigID != nil {
		c, err := s.configs.GetLLMConfig(ctx, *kb.EmbeddingLLMConfigID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && c.OrgID != kb.OrgID) {
			return fmt.Errorf("knowledge: %w: embedding LLM config not found", domain.ErrInvalid)
		}
		if err != nil {
			return fmt.Errorf("knowledge: get embedding config: %w", err)
		}
		if !embed.Supports(c.Provider) {
			return fmt.Errorf("knowledge: %w: provider %s has no embeddings API; choose an OpenAI, Google, "+
				"Ollama or OpenAI-compatible config", domain.ErrInvalid, c.Provider)
		}
		cfg = c
	} else {
		c, err := s.configs.GetDefaultLLMConfig(ctx, kb.OrgID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			return fmt.Errorf("knowledge: get default llm config: %w", err)
		case embed.Supports(c.Provider):
			cfg = c
		}
	}
	if cfg == nil {
		kb.EmbeddingLLMConfigID = nil
		if s.opts.FakeEmbeddings {
			kb.EmbeddingModel = embed.HashModel
		} else {
			kb.EmbeddingModel = "" // text-only search
		}
		return nil
	}
	kb.EmbeddingLLMConfigID = &cfg.ID
	if kb.EmbeddingModel == "" {
		kb.EmbeddingModel = embed.DefaultModel(cfg.Provider)
	}
	if kb.EmbeddingModel == "" {
		return fmt.Errorf("knowledge: %w: embeddingModel is required for %s configs", domain.ErrInvalid, cfg.Provider)
	}
	return nil
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ---------------------------------------------------------------------------
// Documents
// ---------------------------------------------------------------------------

// AddDocument stores an uploaded file (status processing) and ingests it in
// the background. r is fully read before AddDocument returns. Unsupported
// types, empty and oversized (> 20 MB) files are domain.ErrInvalid.
func (s *Service) AddDocument(ctx context.Context, kbID uuid.UUID, filename, mime string, r io.Reader) (*domain.KnowledgeDocument, error) {
	filename = cleanFilename(filename, "document")
	if err := extract.CheckSupported(filename, mime); err != nil {
		return nil, fmt.Errorf("knowledge: %w", err)
	}
	format, _ := extract.Detect(filename, mime)
	if mime == "" || strings.HasPrefix(mime, "application/octet-stream") {
		mime = extract.MIMEType(format)
	}
	raw, err := io.ReadAll(io.LimitReader(r, extract.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("knowledge: read upload: %w", err)
	}
	if len(raw) > extract.MaxBytes {
		return nil, fmt.Errorf("knowledge: %w: file is larger than %d MB", domain.ErrInvalid, extract.MaxBytes>>20)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("knowledge: %w: file is empty", domain.ErrInvalid)
	}
	return s.addDocument(ctx, kbID, filename, mime, int64(len(raw)), job{raw: raw})
}

// AddText stores pasted text as a document and ingests it in the background.
func (s *Service) AddText(ctx context.Context, kbID uuid.UUID, filename, text string) (*domain.KnowledgeDocument, error) {
	filename = cleanFilename(filename, "text.txt")
	text = extract.Clean(text)
	if text == "" {
		return nil, fmt.Errorf("knowledge: %w: text is empty", domain.ErrInvalid)
	}
	if len(text) > extract.MaxBytes {
		return nil, fmt.Errorf("knowledge: %w: text is larger than %d MB", domain.ErrInvalid, extract.MaxBytes>>20)
	}
	return s.addDocument(ctx, kbID, filename, "text/plain", int64(len(text)), job{text: &text})
}

func (s *Service) addDocument(ctx context.Context, kbID uuid.UUID, filename, mime string, size int64, j job) (*domain.KnowledgeDocument, error) {
	kb, err := s.repo.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: add document: %w", err)
	}
	d := &domain.KnowledgeDocument{KnowledgeBaseID: kb.ID, OrgID: kb.OrgID, Filename: filename, MimeType: mime,
		SizeBytes: size, Status: domain.DocumentProcessing}
	if err := s.repo.CreateDocument(ctx, d); err != nil {
		return nil, fmt.Errorf("knowledge: add document: %w", err)
	}
	j.docID, j.filename, j.mime = d.ID, filename, mime
	if err := s.enqueue(ctx, j); err != nil {
		s.fail(d, "Could not queue the document for processing; reprocess it later.")
		return nil, err
	}
	return d, nil
}

// Reprocess re-chunks and re-embeds a document from its stored text in the
// background. domain.ErrConflict when no extracted text is stored (the
// document never finished extraction: upload it again).
func (s *Service) Reprocess(ctx context.Context, docID uuid.UUID) error {
	d, err := s.repo.GetDocument(ctx, docID)
	if err != nil {
		return fmt.Errorf("knowledge: reprocess: %w", err)
	}
	if s.isActive(docID) {
		return nil // already queued; it will pick up the current settings
	}
	if s.texts == nil {
		return fmt.Errorf("knowledge: reprocess: %w: document text is not stored", domain.ErrConflict)
	}
	text, err := s.texts.DocumentText(ctx, docID)
	if err != nil {
		return fmt.Errorf("knowledge: reprocess: %w", err)
	}
	if text == "" {
		return fmt.Errorf("knowledge: reprocess: %w: the document text is not available; upload the file again",
			domain.ErrConflict)
	}
	d.Status, d.Error = domain.DocumentProcessing, ""
	if err := s.repo.UpdateDocument(ctx, d); err != nil {
		return fmt.Errorf("knowledge: reprocess: %w", err)
	}
	return s.enqueue(ctx, job{docID: docID, filename: d.Filename, mime: d.MimeType})
}

// reprocessAll re-ingests every document of a base (best effort, logged).
func (s *Service) reprocessAll(ctx context.Context, kbID uuid.UUID) {
	docs, err := s.repo.ListDocuments(ctx, kbID)
	if err != nil {
		s.log.Warn().Err(err).Str("kb_id", kbID.String()).Msg("list documents for reprocessing")
		return
	}
	for _, d := range docs {
		if err := s.Reprocess(ctx, d.ID); err != nil {
			s.log.Warn().Err(err).Str("doc_id", d.ID.String()).Msg("reprocess after base update")
		}
	}
}

// GetDocument returns a document.
func (s *Service) GetDocument(ctx context.Context, id uuid.UUID) (*domain.KnowledgeDocument, error) {
	d, err := s.repo.GetDocument(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("knowledge: get document: %w", err)
	}
	return d, nil
}

// ListDocuments lists a base's documents.
func (s *Service) ListDocuments(ctx context.Context, kbID uuid.UUID) ([]domain.KnowledgeDocument, error) {
	list, err := s.repo.ListDocuments(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: list documents: %w", err)
	}
	return list, nil
}

// DeleteDocument removes a document and its chunks.
func (s *Service) DeleteDocument(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.DeleteDocument(ctx, id); err != nil {
		return fmt.Errorf("knowledge: delete document: %w", err)
	}
	return nil
}

// ListChunks pages a document's chunks when the repository supports it
// (empty otherwise).
func (s *Service) ListChunks(ctx context.Context, docID uuid.UUID, limit, offset int) ([]domain.KnowledgeChunk, int, error) {
	cl, ok := s.repo.(ChunkLister)
	if !ok {
		return []domain.KnowledgeChunk{}, 0, nil
	}
	return cl.ListChunks(ctx, docID, limit, offset)
}

func cleanFilename(name, def string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	if name == "." || name == "/" || name == "" {
		name = def
	}
	if r := []rune(name); len(r) > 255 {
		name = string(r[:255])
	}
	return name
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Search returns the k best passages for query (k defaults to 5, max 50).
// Hybrid mode fuses the top 20 by vector similarity with the top 20
// full-text matches (RRF); it degrades to "text" when the base has no
// embeddings or the embedder fails (logged).
func (s *Service) Search(ctx context.Context, kbID uuid.UUID, query string, k int) ([]domain.KnowledgeHit, string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "", fmt.Errorf("knowledge: %w: query is empty", domain.ErrInvalid)
	}
	if k <= 0 {
		k = DefaultK
	}
	if k > MaxK {
		k = MaxK
	}
	kb, err := s.repo.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return nil, "", fmt.Errorf("knowledge: search: %w", err)
	}
	textHits, err := s.repo.SearchText(ctx, kbID, query, candidates)
	if err != nil {
		return nil, "", fmt.Errorf("knowledge: search text: %w", err)
	}
	vecHits, ok := s.vectorHits(ctx, kb, query)
	if !ok {
		return fuseRRF(k, textHits), ModeText, nil
	}
	return fuseRRF(k, vecHits, textHits), ModeHybrid, nil
}

// vectorHits embeds the query and runs the vector search; ok is false when
// the base has no vectors or anything fails.
func (s *Service) vectorHits(ctx context.Context, kb *domain.KnowledgeBase, query string) ([]domain.KnowledgeHit, bool) {
	if kb.EmbeddingDims == 0 {
		return nil, false
	}
	logger := s.log.With().Str("kb_id", kb.ID.String()).Logger()
	e, err := s.embedderFor(ctx, kb)
	if err != nil || e == nil {
		logger.Warn().Err(err).Msg("knowledge search: no embedder, falling back to text search")
		return nil, false
	}
	qctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	vec, err := embed.Query(qctx, e, query)
	if err != nil {
		logger.Warn().Err(err).Msg("knowledge search: query embedding failed, falling back to text search")
		return nil, false
	}
	if len(vec) != kb.EmbeddingDims {
		logger.Warn().Int("got", len(vec)).Int("want", kb.EmbeddingDims).
			Msg("knowledge search: embedding size mismatch, falling back to text search")
		return nil, false
	}
	hits, err := s.repo.SearchVector(ctx, kb.ID, vec, candidates)
	if err != nil {
		logger.Warn().Err(err).Msg("knowledge search: vector search failed, falling back to text search")
		return nil, false
	}
	return hits, true
}

// ContextText returns the base's text for knowledgeMode "context", capped at
// Options.ContextMaxChars.
func (s *Service) ContextText(ctx context.Context, kbID uuid.UUID) (string, bool, error) {
	if _, err := s.repo.GetKnowledgeBase(ctx, kbID); err != nil {
		return "", false, fmt.Errorf("knowledge: context text: %w", err)
	}
	text, truncated, err := s.repo.AllChunksText(ctx, kbID, s.opts.ContextMaxChars)
	if err != nil {
		return "", false, fmt.Errorf("knowledge: context text: %w", err)
	}
	return text, truncated, nil
}

// embedderFor returns the embedder of a base, or nil for a text-only base.
// It errors when a configured embedder cannot be built.
func (s *Service) embedderFor(ctx context.Context, kb *domain.KnowledgeBase) (domain.Embedder, error) {
	hash := func() domain.Embedder {
		if kb.EmbeddingDims == 0 || kb.EmbeddingDims == HashDims {
			return embed.NewHash(HashDims)
		}
		return nil
	}
	if kb.EmbeddingModel == embed.HashModel {
		return embed.NewHash(HashDims), nil
	}
	var cfg *domain.LLMConfig
	if kb.EmbeddingLLMConfigID != nil {
		c, err := s.configs.GetLLMConfig(ctx, *kb.EmbeddingLLMConfigID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("get embedding config: %w", err)
		default:
			cfg = c
		}
	}
	if cfg == nil || !embed.Supports(cfg.Provider) || kb.EmbeddingModel == "" {
		if s.opts.FakeEmbeddings {
			return hash(), nil
		}
		return nil, nil
	}
	e, err := s.factory(cfg, kb.EmbeddingModel)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// Ingestion
// ---------------------------------------------------------------------------

// enqueue hands a job to the workers; a job for a document already in
// flight replaces any earlier pending one and runs after it.
func (s *Service) enqueue(ctx context.Context, j job) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("knowledge: service is closed")
	}
	if s.active[j.docID] {
		if prev, ok := s.pending[j.docID]; ok && j.raw == nil && j.text == nil {
			// Keep the fresher source of an earlier pending upload.
			j.raw, j.text = prev.raw, prev.text
		}
		s.pending[j.docID] = j
		s.mu.Unlock()
		return nil
	}
	s.active[j.docID] = true
	s.mu.Unlock()
	select {
	case s.jobs <- j:
		return nil
	case <-ctx.Done():
		s.release(j.docID)
		return fmt.Errorf("knowledge: enqueue: %w", ctx.Err())
	case <-s.ctx.Done():
		s.release(j.docID)
		return fmt.Errorf("knowledge: service is closed")
	}
}

// release marks a document idle, returning a pending job for it if any.
func (s *Service) release(docID uuid.UUID) (job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.pending[docID]; ok {
		delete(s.pending, docID)
		return j, true
	}
	delete(s.active, docID)
	return job{}, false
}

func (s *Service) isActive(docID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[docID]
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			s.drainQueue()
			return
		case j := <-s.jobs:
			for {
				s.ingest(j)
				next, ok := s.release(j.docID)
				if !ok {
					break
				}
				j = next
			}
		}
	}
}

// drainQueue marks queued-but-unstarted documents failed on shutdown.
func (s *Service) drainQueue() {
	for {
		select {
		case j := <-s.jobs:
			s.fail(&domain.KnowledgeDocument{ID: j.docID}, "Processing was interrupted; reprocess the document.")
			s.mu.Lock()
			delete(s.active, j.docID)
			delete(s.pending, j.docID)
			s.mu.Unlock()
		default:
			return
		}
	}
}

// ingest runs one job: extract → chunk → embed → store → status.
func (s *Service) ingest(j job) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(s.ctx, jobTimeout)
	defer cancel()
	logger := s.log.With().Str("doc_id", j.docID.String()).Logger()

	d, err := s.repo.GetDocument(ctx, j.docID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			logger.Error().Err(err).Msg("knowledge ingest: load document")
		}
		return // deleted meanwhile
	}
	msg, err := s.runIngest(ctx, d, j)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			logger.Debug().Err(err).Msg("knowledge ingest: document or base deleted during ingestion")
			return
		}
		if s.ctx.Err() != nil {
			msg = "Processing was interrupted; reprocess the document."
		}
		logger.Warn().Err(err).Str("reason", msg).Dur("took", time.Since(start)).Msg("knowledge ingest failed")
		s.fail(d, msg)
		return
	}
	logger.Info().Int("chunks", d.ChunkCount).Int("chars", d.CharCount).Dur("took", time.Since(start)).
		Msg("knowledge document ready")
}

// runIngest does the work; on failure it returns a user-readable message
// and the underlying error.
func (s *Service) runIngest(ctx context.Context, d *domain.KnowledgeDocument, j job) (string, error) {
	kb, err := s.repo.GetKnowledgeBase(ctx, d.KnowledgeBaseID)
	if err != nil {
		return "Internal error while loading the knowledge base.", err
	}

	var text string
	stored := false // text came from the text store: no need to save it again
	switch {
	case j.raw != nil:
		text, err = extract.Bytes(j.filename, j.mime, j.raw)
		if err != nil {
			var ee *extract.Error
			if errors.As(err, &ee) {
				return ee.Msg, err
			}
			return "The file could not be read.", err
		}
	case j.text != nil:
		text = *j.text
	default:
		if s.texts == nil {
			return "The document text is not available; upload the file again.", errors.New("no text store")
		}
		text, err = s.texts.DocumentText(ctx, d.ID)
		if err != nil {
			return "Internal error while loading the document text.", err
		}
		stored = true
	}
	text = extract.Clean(text)
	if text == "" {
		return "The document contains no text.", errors.New("empty text")
	}
	if s.texts != nil && !stored {
		if err := s.texts.SetDocumentText(ctx, d.ID, text); err != nil {
			return "Internal error while storing the document text.", err
		}
	}

	parts := chunk.Split(text, kb.ChunkSize, kb.ChunkOverlap)
	if len(parts) == 0 {
		return "The document contains no text.", errors.New("no chunks")
	}
	chunks := make([]domain.KnowledgeChunk, len(parts))
	for i, p := range parts {
		chunks[i] = domain.KnowledgeChunk{DocumentID: d.ID, KnowledgeBaseID: kb.ID, Seq: p.Seq,
			Heading: p.Heading, Content: p.Content}
	}

	e, err := s.embedderFor(ctx, kb)
	if err != nil {
		return "Embedding is not configured correctly: " + userText(err), err
	}
	if e != nil {
		if err := embedChunks(ctx, e, chunks); err != nil {
			return "Embedding failed: " + userText(err), err
		}
	}

	if err := s.repo.ReplaceChunks(ctx, d.ID, chunks); err != nil {
		if errors.Is(err, domain.ErrInvalid) && e != nil {
			return fmt.Sprintf("The embedding model returns %d dimensions but this knowledge base stores %d; "+
				"create a new knowledge base for this model.", len(chunks[0].Embedding), kb.EmbeddingDims), err
		}
		return "Internal error while storing the chunks.", err
	}
	d.Status, d.Error = domain.DocumentReady, ""
	d.ChunkCount, d.CharCount = len(chunks), utf8.RuneCountInString(text)
	if err := s.repo.UpdateDocument(ctx, d); err != nil {
		return "Internal error while updating the document.", err
	}
	return "", nil
}

// embedChunks embeds "heading\ncontent" of every chunk in batches.
func embedChunks(ctx context.Context, e domain.Embedder, chunks []domain.KnowledgeChunk) error {
	for start := 0; start < len(chunks); start += embedBatch {
		end := min(start+embedBatch, len(chunks))
		inputs := make([]string, 0, end-start)
		for _, c := range chunks[start:end] {
			if c.Heading != "" {
				inputs = append(inputs, c.Heading+"\n"+c.Content)
			} else {
				inputs = append(inputs, c.Content)
			}
		}
		vecs, err := e.Embed(ctx, inputs)
		if err != nil {
			return err
		}
		if len(vecs) != len(inputs) {
			return fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(inputs))
		}
		for i, v := range vecs {
			chunks[start+i].Embedding = v
		}
	}
	return nil
}

// fail marks a document failed with a user-readable message (best effort,
// even while shutting down).
func (s *Service) fail(d *domain.KnowledgeDocument, msg string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), statusTimeout)
	defer cancel()
	cur, err := s.repo.GetDocument(ctx, d.ID)
	if err != nil {
		return // deleted
	}
	cur.Status, cur.Error = domain.DocumentFailed, msg
	if err := s.repo.UpdateDocument(ctx, cur); err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.log.Error().Err(err).Str("doc_id", d.ID.String()).Msg("knowledge: mark document failed")
	}
}

// userText strips package prefixes ("embed: ") from an error message.
func userText(err error) string {
	msg := err.Error()
	for _, p := range []string{"embed: ", "knowledge: "} {
		msg = strings.ReplaceAll(msg, p, "")
	}
	if r := []rune(msg); len(r) > 400 {
		msg = string(r[:400]) + "…"
	}
	return msg
}
