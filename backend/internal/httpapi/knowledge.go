package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

const (
	// maxKnowledgeUpload is the largest document accepted (file or pasted text).
	maxKnowledgeUpload = 20 << 20
	// knowledgeUploadSlack covers multipart / JSON framing around the payload.
	knowledgeUploadSlack = 1 << 20

	maxKnowledgeNameLen     = 100
	maxKnowledgeDescLen     = 2000
	maxKnowledgeFilenameLen = 255
	maxKnowledgeQueryLen    = 2000

	defaultChunkSize    = 1200
	defaultChunkOverlap = 200
	minChunkSize        = 200
	maxChunkSize        = 8000

	defaultSearchK = 5
	maxSearchK     = 20

	chunkPreviewPage = 50
)

// knowledgeMimeTypes is the upload whitelist: extension → canonical MIME type.
var knowledgeMimeTypes = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".txt":  "text/plain",
	".md":   "text/markdown",
	".csv":  "text/csv",
}

func errKnowledgeNotConfigured() error { return errNotConfigured("knowledge base service") }

// getKnowledgeBase fetches a base via the service, or the repository when the
// service is absent.
func (s *server) getKnowledgeBase(ctx context.Context, id uuid.UUID) (*domain.KnowledgeBase, error) {
	switch {
	case s.d.Knowledge != nil:
		return s.d.Knowledge.GetBase(ctx, id)
	case s.d.KnowledgeRepo != nil:
		return s.d.KnowledgeRepo.GetKnowledgeBase(ctx, id)
	}
	return nil, errKnowledgeNotConfigured()
}

// loadKnowledgeBase returns the org's base or a 404.
func (s *server) loadKnowledgeBase(ctx context.Context, orgID, id uuid.UUID) (*domain.KnowledgeBase, error) {
	kb, err := s.getKnowledgeBase(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (kb == nil || kb.OrgID != orgID)) {
		return nil, errNotFound("knowledge base")
	}
	if err != nil {
		return nil, fmt.Errorf("get knowledge base: %w", err)
	}
	return kb, nil
}

// loadKnowledgeDocument returns the org's document or a 404.
func (s *server) loadKnowledgeDocument(ctx context.Context, orgID, id uuid.UUID) (*domain.KnowledgeDocument, error) {
	if s.d.KnowledgeRepo == nil {
		return nil, errNotConfigured("knowledge repository")
	}
	d, err := s.d.KnowledgeRepo.GetDocument(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (d == nil || d.OrgID != orgID)) {
		return nil, errNotFound("knowledge document")
	}
	if err != nil {
		return nil, fmt.Errorf("get knowledge document: %w", err)
	}
	return d, nil
}

// knowledgeErr maps service errors: conflicts get a helpful message, the
// rest go through writeErr's generic mapping.
func knowledgeErr(err error, op string) error {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return err
	case errors.Is(err, domain.ErrConflict):
		return errConflict("embedding settings are locked once the knowledge base has chunks")
	case errors.Is(err, domain.ErrNotFound):
		return errNotFound("knowledge base")
	case errors.Is(err, domain.ErrInvalid):
		return errInvalid("%s", err.Error())
	}
	return fmt.Errorf("%s: %w", op, err)
}

// ---------------------------------------------------------------------------
// Knowledge bases
// ---------------------------------------------------------------------------

// knowledgeBaseBody is the POST/PUT body. Pointers tell "absent" (keep the
// stored value on PUT, default on POST) from an explicit value. An empty
// embeddingLlmConfigId / embeddingModel means "default" on POST and "keep"
// on PUT, so re-sending a form whose embedding fields were left blank never
// trips the embedding lock.
type knowledgeBaseBody struct {
	Name                 string  `json:"name"`
	Description          *string `json:"description"`
	EmbeddingLLMConfigID *string `json:"embeddingLlmConfigId"`
	EmbeddingModel       *string `json:"embeddingModel"`
	ChunkSize            *int    `json:"chunkSize"`
	ChunkOverlap         *int    `json:"chunkOverlap"`
}

// applyKnowledgeBaseBody validates b onto kb. It reports whether the
// embedding settings (config or model) changed.
func (s *server) applyKnowledgeBaseBody(ctx context.Context, b knowledgeBaseBody, kb *domain.KnowledgeBase) (bool, error) {
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return false, errInvalid("name is required")
	}
	if utf8.RuneCountInString(name) > maxKnowledgeNameLen {
		return false, errInvalid("name must be at most %d characters", maxKnowledgeNameLen)
	}
	desc := kb.Description
	if b.Description != nil {
		desc = strings.TrimSpace(*b.Description)
	}
	if utf8.RuneCountInString(desc) > maxKnowledgeDescLen {
		return false, errInvalid("description must be at most %d characters", maxKnowledgeDescLen)
	}
	embCfg := kb.EmbeddingLLMConfigID
	if b.EmbeddingLLMConfigID != nil {
		id, err := parseOptUUID(*b.EmbeddingLLMConfigID, "embeddingLlmConfigId")
		if err != nil {
			return false, err
		}
		if id != nil {
			if _, err := s.loadLLMConfig(ctx, kb.OrgID, *id); err != nil {
				return false, asInvalidRef(err, "embeddingLlmConfigId")
			}
			embCfg = id
		}
	}
	model := kb.EmbeddingModel
	if b.EmbeddingModel != nil {
		if m := strings.TrimSpace(*b.EmbeddingModel); m != "" {
			model = m
		}
	}
	if len(model) > 200 {
		return false, errInvalid("embeddingModel must be at most 200 characters")
	}
	size := kb.ChunkSize
	if b.ChunkSize != nil && *b.ChunkSize != 0 {
		size = *b.ChunkSize
	}
	if size == 0 {
		size = defaultChunkSize
	}
	if size < minChunkSize || size > maxChunkSize {
		return false, errInvalid("chunkSize must be between %d and %d", minChunkSize, maxChunkSize)
	}
	overlap := kb.ChunkOverlap
	if b.ChunkOverlap != nil {
		overlap = *b.ChunkOverlap
	} else if kb.ChunkSize == 0 {
		overlap = min(defaultChunkOverlap, size/2)
	}
	if overlap < 0 || overlap > size/2 {
		return false, errInvalid("chunkOverlap must be between 0 and %d (half of chunkSize)", size/2)
	}
	changed := !sameUUID(embCfg, kb.EmbeddingLLMConfigID) || model != kb.EmbeddingModel
	kb.Name, kb.Description = name, desc
	kb.EmbeddingLLMConfigID, kb.EmbeddingModel = embCfg, model
	kb.ChunkSize, kb.ChunkOverlap = size, overlap
	return changed, nil
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *server) listKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	items, err := s.d.Knowledge.ListBases(r.Context(), claimsOf(r).OrgID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list knowledge bases: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(items, len(items)))
}

func (s *server) getKnowledgeBaseHandler(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	kb, err := s.loadKnowledgeBase(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	docs := []domain.KnowledgeDocument{}
	if s.d.KnowledgeRepo != nil {
		list, err := s.d.KnowledgeRepo.ListDocuments(ctx, kb.ID)
		if err != nil {
			s.writeErr(w, r, fmt.Errorf("list knowledge documents: %w", err))
			return
		}
		if list != nil {
			docs = list
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledgeBase": kb, "documents": docs})
}

func (s *server) createKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	var b knowledgeBaseBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	now := s.now()
	kb := &domain.KnowledgeBase{ID: uuid.New(), OrgID: claimsOf(r).OrgID, CreatedAt: now, UpdatedAt: now}
	if _, err := s.applyKnowledgeBaseBody(ctx, b, kb); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Knowledge.CreateBase(ctx, kb); err != nil {
		s.writeErr(w, r, knowledgeErr(err, "create knowledge base"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"knowledgeBase": kb})
}

func (s *server) updateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var b knowledgeBaseBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	kb, err := s.loadKnowledgeBase(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	embeddingChanged, err := s.applyKnowledgeBaseBody(ctx, b, kb)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if embeddingChanged && kb.ChunkCount > 0 {
		s.writeErr(w, r, knowledgeErr(domain.ErrConflict, ""))
		return
	}
	kb.UpdatedAt = s.now()
	if err := s.d.Knowledge.UpdateBase(ctx, kb); err != nil {
		s.writeErr(w, r, knowledgeErr(err, "update knowledge base"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledgeBase": kb})
}

func (s *server) deleteKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.loadKnowledgeBase(ctx, claimsOf(r).OrgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Knowledge.DeleteBase(ctx, id); err != nil {
		s.writeErr(w, r, knowledgeErr(err, "delete knowledge base"))
		return
	}
	noContent(w)
}

// ---------------------------------------------------------------------------
// Documents
// ---------------------------------------------------------------------------

type pastedText struct {
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

func (s *server) createKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	kb, err := s.loadKnowledgeBase(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var doc *domain.KnowledgeDocument
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(mt, "multipart/") {
		doc, err = s.addUploadedDocument(w, r, kb.ID)
	} else {
		doc, err = s.addPastedDocument(w, r, kb.ID)
	}
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"document": doc})
}

func (s *server) addUploadedDocument(w http.ResponseWriter, r *http.Request, kbID uuid.UUID) (*domain.KnowledgeDocument, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxKnowledgeUpload+knowledgeUploadSlack)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if isTooLarge(err) {
			return nil, errInvalid("file must be at most %d MB", maxKnowledgeUpload>>20)
		}
		return nil, errInvalid("expected multipart/form-data: %v", err)
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return nil, errInvalid("file is required")
	}
	defer f.Close()
	name := cleanFilename(hdr.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	mimeType, ok := knowledgeMimeTypes[ext]
	if !ok {
		return nil, errInvalid("unsupported file type %q (allowed: pdf, docx, txt, md, csv)", ext)
	}
	if hdr.Size > maxKnowledgeUpload {
		return nil, errInvalid("file must be at most %d MB", maxKnowledgeUpload>>20)
	}
	if hdr.Size == 0 {
		return nil, errInvalid("file is empty")
	}
	doc, err := s.d.Knowledge.AddDocument(r.Context(), kbID, name, mimeType, io.LimitReader(f, maxKnowledgeUpload))
	if err != nil {
		return nil, knowledgeErr(err, "add knowledge document")
	}
	return doc, nil
}

func (s *server) addPastedDocument(w http.ResponseWriter, r *http.Request, kbID uuid.UUID) (*domain.KnowledgeDocument, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxKnowledgeUpload+knowledgeUploadSlack)
	var b pastedText
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		switch {
		case errors.Is(err, io.EOF):
			return nil, errInvalid("request body is empty")
		case isTooLarge(err):
			return nil, errInvalid("text must be at most %d MB", maxKnowledgeUpload>>20)
		}
		return nil, errInvalid("expected multipart file or JSON {filename, text}: %v", err)
	}
	text := strings.TrimSpace(b.Text)
	if text == "" {
		return nil, errInvalid("text is required")
	}
	if len(text) > maxKnowledgeUpload {
		return nil, errInvalid("text must be at most %d MB", maxKnowledgeUpload>>20)
	}
	name := cleanFilename(b.Filename)
	if name == "" {
		name = "pasted-text.txt"
	}
	doc, err := s.d.Knowledge.AddText(r.Context(), kbID, name, text)
	if err != nil {
		return nil, knowledgeErr(err, "add knowledge text")
	}
	return doc, nil
}

func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// cleanFilename strips any directory part and caps the length (keeping the
// extension).
func cleanFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if utf8.RuneCountInString(name) > maxKnowledgeFilenameLen {
		ext := filepath.Ext(name)
		runes := []rune(strings.TrimSuffix(name, ext))
		keep := max(maxKnowledgeFilenameLen-utf8.RuneCountInString(ext), 1)
		if keep < len(runes) {
			runes = runes[:keep]
		}
		name = string(runes) + ext
	}
	return name
}

func (s *server) listKnowledgeDocuments(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	if s.d.KnowledgeRepo == nil {
		s.writeErr(w, r, errNotConfigured("knowledge repository"))
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	kb, err := s.loadKnowledgeBase(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	docs, err := s.d.KnowledgeRepo.ListDocuments(ctx, kb.ID)
	if err != nil {
		s.writeErr(w, r, fmt.Errorf("list knowledge documents: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, newList(docs, len(docs)))
}

// chunkPreview is the browser view of a chunk (no embedding).
type chunkPreview struct {
	ID      uuid.UUID `json:"id"`
	Seq     int       `json:"seq"`
	Heading string    `json:"heading,omitempty"`
	Content string    `json:"content"`
}

func (s *server) chunkLister() ChunkLister {
	if s.d.Chunks != nil {
		return s.d.Chunks
	}
	if cl, ok := s.d.KnowledgeRepo.(ChunkLister); ok {
		return cl
	}
	return nil
}

func (s *server) getKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "docId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1<<30)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	doc, err := s.loadKnowledgeDocument(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	chunks := []chunkPreview{}
	total := 0
	if cl := s.chunkLister(); cl != nil {
		list, n, err := cl.ListChunks(ctx, doc.ID, chunkPreviewPage, offset)
		if err != nil {
			s.writeErr(w, r, fmt.Errorf("list knowledge chunks: %w", err))
			return
		}
		total = n
		for _, c := range list {
			chunks = append(chunks, chunkPreview{ID: c.ID, Seq: c.Seq, Heading: c.Heading, Content: c.Content})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": doc, "chunks": chunks, "chunkTotal": total})
}

func (s *server) deleteKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	id, err := urlID(r, "docId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.loadKnowledgeDocument(ctx, claimsOf(r).OrgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.KnowledgeRepo.DeleteDocument(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, errNotFound("knowledge document"))
			return
		}
		s.writeErr(w, r, fmt.Errorf("delete knowledge document: %w", err))
		return
	}
	noContent(w)
}

func (s *server) reprocessKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	id, err := urlID(r, "docId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.loadKnowledgeDocument(ctx, claimsOf(r).OrgID, id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Knowledge.Reprocess(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeErr(w, r, errNotFound("knowledge document"))
			return
		}
		s.writeErr(w, r, knowledgeErr(err, "reprocess knowledge document"))
		return
	}
	// Return the document as it is now (status "processing" once the
	// service has queued it).
	doc, err := s.loadKnowledgeDocument(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"document": doc})
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

type searchBody struct {
	Query string `json:"query"`
	K     int    `json:"k"`
}

// normalizeSearch trims the query and clamps k to 1..20 (0 → 5).
func normalizeSearch(b searchBody) (string, int, error) {
	q := strings.TrimSpace(b.Query)
	if q == "" {
		return "", 0, errInvalid("query is required")
	}
	if utf8.RuneCountInString(q) > maxKnowledgeQueryLen {
		return "", 0, errInvalid("query must be at most %d characters", maxKnowledgeQueryLen)
	}
	k := b.K
	switch {
	case k == 0:
		k = defaultSearchK
	case k < 1:
		k = 1
	case k > maxSearchK:
		k = maxSearchK
	}
	return q, k, nil
}

func (s *server) searchKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	id, err := urlID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var b searchBody
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	query, k, err := normalizeSearch(b)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	kb, err := s.loadKnowledgeBase(ctx, claimsOf(r).OrgID, id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	start := time.Now()
	hits, mode, err := s.d.Knowledge.Search(ctx, kb.ID, query, k)
	if err != nil {
		s.writeErr(w, r, knowledgeErr(err, "search knowledge base"))
		return
	}
	if hits == nil {
		hits = []domain.KnowledgeHit{}
	}
	if mode == "" {
		mode = "text"
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "latencyMs": time.Since(start).Milliseconds(), "mode": mode})
}

// agentKnowledgeSearch serves POST /internal/agent/knowledge/search. The
// agent token is trusted, so the base is not org-checked.
func (s *server) agentKnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	if s.d.Knowledge == nil {
		s.writeErr(w, r, errKnowledgeNotConfigured())
		return
	}
	var b struct {
		KnowledgeBaseID string `json:"knowledgeBaseId"`
		searchBody
	}
	if err := decodeJSON(w, r, &b); err != nil {
		s.writeErr(w, r, err)
		return
	}
	id, err := parseOptUUID(b.KnowledgeBaseID, "knowledgeBaseId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if id == nil {
		s.writeErr(w, r, errInvalid("knowledgeBaseId is required"))
		return
	}
	query, k, err := normalizeSearch(b.searchBody)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	hits, _, err := s.d.Knowledge.Search(r.Context(), *id, query, k)
	if err != nil {
		s.writeErr(w, r, knowledgeErr(err, "search knowledge base"))
		return
	}
	if hits == nil {
		hits = []domain.KnowledgeHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

// knowledgeInfo is the bootstrap "knowledge" block.
type knowledgeInfo struct {
	ID          uuid.UUID            `json:"id"`
	Name        string               `json:"name"`
	Mode        domain.KnowledgeMode `json:"mode"`
	ContextText string               `json:"contextText,omitempty"`
	Truncated   bool                 `json:"truncated"`
}

// bootstrapKnowledge resolves the profile's knowledge block. Failures are
// logged and yield nil so that a broken knowledge base never keeps the call
// from being answered.
func (s *server) bootstrapKnowledge(ctx context.Context, orgID uuid.UUID, p *domain.AgentProfile) *knowledgeInfo {
	if s.d.Knowledge == nil || p == nil || p.KnowledgeBaseID == nil {
		return nil
	}
	mode := p.KnowledgeMode
	if mode != domain.KnowledgeTool && mode != domain.KnowledgeContext {
		return nil
	}
	log := s.log.With().Str("knowledgeBaseId", p.KnowledgeBaseID.String()).Str("profileId", p.ID.String()).Logger()
	kb, err := s.d.Knowledge.GetBase(ctx, *p.KnowledgeBaseID)
	if err != nil {
		log.Warn().Err(err).Msg("bootstrap: get knowledge base")
		return nil
	}
	if kb == nil || kb.OrgID != orgID {
		log.Warn().Msg("bootstrap: knowledge base missing or of another org")
		return nil
	}
	info := &knowledgeInfo{ID: kb.ID, Name: kb.Name, Mode: mode}
	if mode == domain.KnowledgeContext {
		text, truncated, err := s.d.Knowledge.ContextText(ctx, kb.ID)
		if err != nil {
			log.Warn().Err(err).Msg("bootstrap: knowledge context text")
			return nil
		}
		info.ContextText, info.Truncated = text, truncated
	}
	return info
}

// validateProfileKnowledge checks a profile's knowledge settings. A mode
// other than "off" without a base is normalised to "off".
func (s *server) validateProfileKnowledge(ctx context.Context, orgID uuid.UUID, rawID, rawMode string) (*uuid.UUID, domain.KnowledgeMode, error) {
	mode := domain.KnowledgeMode(strings.ToLower(strings.TrimSpace(rawMode)))
	switch mode {
	case "":
		mode = domain.KnowledgeOff
	case domain.KnowledgeOff, domain.KnowledgeTool, domain.KnowledgeContext:
	default:
		return nil, "", errInvalid("knowledgeMode must be one of off, tool, context")
	}
	id, err := parseOptUUID(rawID, "knowledgeBaseId")
	if err != nil {
		return nil, "", err
	}
	if id == nil {
		return nil, domain.KnowledgeOff, nil
	}
	if s.d.Knowledge == nil && s.d.KnowledgeRepo == nil {
		return nil, "", errInvalid("knowledgeBaseId: knowledge bases are not configured")
	}
	if _, err := s.loadKnowledgeBase(ctx, orgID, *id); err != nil {
		return nil, "", asInvalidRef(err, "knowledgeBaseId")
	}
	return id, mode, nil
}
