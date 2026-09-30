package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/auth"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

type kbResp struct {
	KnowledgeBase domain.KnowledgeBase       `json:"knowledgeBase"`
	Documents     []domain.KnowledgeDocument `json:"documents"`
}

type docResp struct {
	Document   domain.KnowledgeDocument `json:"document"`
	Chunks     []chunkPreview           `json:"chunks"`
	ChunkTotal int                      `json:"chunkTotal"`
}

func (e *env) seedKB(orgID uuid.UUID, name string, mutate ...func(*domain.KnowledgeBase)) domain.KnowledgeBase {
	e.t.Helper()
	kb := domain.KnowledgeBase{ID: uuid.New(), OrgID: orgID, Name: name, EmbeddingModel: "text-embedding-3-small",
		ChunkSize: 1200, ChunkOverlap: 200, CreatedAt: time.Now().UTC()}
	for _, m := range mutate {
		m(&kb)
	}
	require.NoError(e.t, e.kn.CreateKnowledgeBase(context.Background(), &kb))
	return kb
}

func (e *env) seedDoc(kb domain.KnowledgeBase, filename string, chunks int) domain.KnowledgeDocument {
	e.t.Helper()
	d := domain.KnowledgeDocument{ID: uuid.New(), KnowledgeBaseID: kb.ID, OrgID: kb.OrgID, Filename: filename,
		MimeType: "text/plain", Status: domain.DocumentReady, ChunkCount: chunks}
	require.NoError(e.t, e.kn.CreateDocument(context.Background(), &d))
	cs := make([]domain.KnowledgeChunk, 0, chunks)
	for i := range chunks {
		cs = append(cs, domain.KnowledgeChunk{ID: uuid.New(), DocumentID: d.ID, KnowledgeBaseID: kb.ID, Seq: i,
			Content: fmt.Sprintf("chunk %d", i), Heading: "H", Embedding: []float32{1, 2}})
	}
	require.NoError(e.t, e.kn.ReplaceChunks(context.Background(), d.ID, cs))
	return d
}

func TestKnowledgeBaseCRUD(t *testing.T) {
	e := newEnv(t)
	llm := e.seedLLM(e.org.ID, "emb", "sk-emb-0000000000", false, nil)
	otherLLM := e.seedLLM(e.org2.ID, "x", "sk-x-000000000000", false, nil)

	// Validation.
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-bases", e.adminTok, map[string]any{"name": " "}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-bases", e.adminTok,
		map[string]any{"name": "KB", "embeddingLlmConfigId": otherLLM.ID}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-bases", e.adminTok,
		map[string]any{"name": "KB", "chunkSize": 50}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-bases", e.adminTok,
		map[string]any{"name": "KB", "chunkSize": 1000, "chunkOverlap": 900}), http.StatusBadRequest, "invalid")
	// Operators cannot write.
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-bases", e.opTok, map[string]any{"name": "KB"}), http.StatusForbidden, "forbidden")

	w := e.do(http.MethodPost, "/api/knowledge-bases", e.adminTok, map[string]any{
		"name": "Гарын авлага", "description": "manual", "embeddingLlmConfigId": llm.ID.String(),
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	kb := decode[kbResp](t, w).KnowledgeBase
	assert.Equal(t, e.org.ID, kb.OrgID)
	assert.Equal(t, "Гарын авлага", kb.Name)
	assert.Equal(t, 1200, kb.ChunkSize)
	assert.Equal(t, 200, kb.ChunkOverlap)
	assert.Equal(t, "text-embedding-3-small", kb.EmbeddingModel)
	require.NotNil(t, kb.EmbeddingLLMConfigID)
	assert.Equal(t, llm.ID, *kb.EmbeddingLLMConfigID)

	// List + get (any role).
	e.seedKB(e.org2.ID, "foreign")
	w = e.do(http.MethodGet, "/api/knowledge-bases", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	lst := decode[list[domain.KnowledgeBase]](t, w)
	require.Len(t, lst.Items, 1)
	assert.Equal(t, kb.ID, lst.Items[0].ID)

	doc := e.seedDoc(kb, "a.txt", 1)
	w = e.do(http.MethodGet, "/api/knowledge-bases/"+kb.ID.String(), e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := decode[kbResp](t, w)
	assert.Equal(t, kb.ID, got.KnowledgeBase.ID)
	require.Len(t, got.Documents, 1)
	assert.Equal(t, doc.ID, got.Documents[0].ID)

	// Update keeps omitted fields.
	w = e.do(http.MethodPut, "/api/knowledge-bases/"+kb.ID.String(), e.adminTok, map[string]any{"name": "Renamed", "chunkSize": 800})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	up := decode[kbResp](t, w).KnowledgeBase
	assert.Equal(t, "Renamed", up.Name)
	assert.Equal(t, "manual", up.Description)
	assert.Equal(t, 800, up.ChunkSize)
	assert.Equal(t, 200, up.ChunkOverlap)
	require.NotNil(t, up.EmbeddingLLMConfigID)

	// Delete.
	requireErr(t, e.do(http.MethodDelete, "/api/knowledge-bases/"+kb.ID.String(), e.opTok, nil), http.StatusForbidden, "forbidden")
	w = e.do(http.MethodDelete, "/api/knowledge-bases/"+kb.ID.String(), e.adminTok, nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	requireErr(t, e.do(http.MethodGet, "/api/knowledge-bases/"+kb.ID.String(), e.adminTok, nil), http.StatusNotFound, "not_found")
	requireErr(t, e.do(http.MethodGet, "/api/knowledge-bases/not-a-uuid", e.adminTok, nil), http.StatusBadRequest, "invalid")
}

func TestKnowledgeBaseEmbeddingLock(t *testing.T) {
	e := newEnv(t)
	llm := e.seedLLM(e.org.ID, "emb", "sk-emb-0000000000", false, nil)
	kb := e.seedKB(e.org.ID, "KB", func(kb *domain.KnowledgeBase) { kb.ChunkCount = 10; kb.EmbeddingDims = 1536 })
	path := "/api/knowledge-bases/" + kb.ID.String()

	// Changing the model or the embedding config of a base with chunks → 409.
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": "KB", "embeddingModel": "text-embedding-3-large"}),
		http.StatusConflict, "conflict")
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": "KB", "embeddingLlmConfigId": llm.ID}),
		http.StatusConflict, "conflict")
	// Re-sending the same (or blank) embedding settings is fine.
	w := e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": "KB2", "embeddingModel": "text-embedding-3-small", "embeddingLlmConfigId": ""})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// A conflict reported by the service is passed through as 409.
	e.kn.updateErr = fmt.Errorf("update base: %w", domain.ErrConflict)
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": "KB3"}), http.StatusConflict, "conflict")
	// ErrInvalid from the service → 400.
	e.kn.updateErr = fmt.Errorf("%w: openai_compatible needs embeddingModel", domain.ErrInvalid)
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, map[string]any{"name": "KB3"}), http.StatusBadRequest, "invalid")
}

func TestKnowledgeOrgIsolation(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "Mine")
	doc := e.seedDoc(kb, "a.txt", 2)
	kbPath := "/api/knowledge-bases/" + kb.ID.String()
	docPath := "/api/knowledge-documents/" + doc.ID.String()

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, kbPath, nil},
		{http.MethodPut, kbPath, map[string]any{"name": "hijack"}},
		{http.MethodDelete, kbPath, nil},
		{http.MethodGet, kbPath + "/documents", nil},
		{http.MethodPost, kbPath + "/documents", map[string]any{"filename": "x.txt", "text": "hi"}},
		{http.MethodPost, kbPath + "/search", map[string]any{"query": "hi"}},
		{http.MethodGet, docPath, nil},
		{http.MethodDelete, docPath, nil},
		{http.MethodPost, docPath + "/reprocess", nil},
	} {
		w := e.do(tc.method, tc.path, e.othTok, tc.body)
		requireErr(t, w, http.StatusNotFound, "not_found")
	}
	// Nothing changed.
	got, err := e.kn.GetKnowledgeBase(context.Background(), kb.ID)
	require.NoError(t, err)
	assert.Equal(t, "Mine", got.Name)
	_, err = e.kn.GetDocument(context.Background(), doc.ID)
	require.NoError(t, err)
	assert.Empty(t, e.kn.reprocessed)
}

func TestKnowledgeNotConfigured(t *testing.T) {
	e := newEnv(t, func(d *Deps, _ *Config) { d.Knowledge, d.KnowledgeRepo = nil, nil })
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/knowledge-bases"},
		{http.MethodPost, "/api/knowledge-bases"},
		{http.MethodGet, "/api/knowledge-documents/" + uuid.NewString()},
	} {
		w := e.do(tc.method, tc.path, e.adminTok, map[string]any{"name": "x"})
		requireErr(t, w, http.StatusInternalServerError, "internal")
	}
	w := e.agent(http.MethodPost, "/internal/agent/knowledge/search", map[string]any{"knowledgeBaseId": uuid.New(), "query": "q"})
	requireErr(t, w, http.StatusInternalServerError, "internal")
}

func TestKnowledgeDocumentUpload(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "KB")
	path := "/api/knowledge-bases/" + kb.ID.String() + "/documents"

	// Multipart file.
	w := e.upload(path, e.adminTok, nil, "Гарын авлага.PDF", []byte("%PDF-1.4 fake"))
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	d := decode[docResp](t, w).Document
	assert.Equal(t, "Гарын авлага.PDF", d.Filename)
	assert.Equal(t, "application/pdf", d.MimeType)
	assert.Equal(t, domain.DocumentProcessing, d.Status)
	assert.Equal(t, kb.ID, d.KnowledgeBaseID)
	assert.Equal(t, []byte("%PDF-1.4 fake"), e.kn.uploaded(d.ID))

	for ext, mt := range map[string]string{
		"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"txt":  "text/plain", "md": "text/markdown", "csv": "text/csv",
	} {
		w = e.upload(path, e.adminTok, nil, "dir/file."+ext, []byte("content"))
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		d := decode[docResp](t, w).Document
		assert.Equal(t, mt, d.MimeType, ext)
		assert.Equal(t, "file."+ext, d.Filename, "directory part stripped")
	}

	// Rejected extensions, missing and empty files.
	for _, name := range []string{"evil.exe", "sheet.xlsx", "noext", "doc.doc"} {
		requireErr(t, e.upload(path, e.adminTok, nil, name, []byte("x")), http.StatusBadRequest, "invalid")
	}
	requireErr(t, e.upload(path, e.adminTok, map[string]string{"a": "b"}, "", nil), http.StatusBadRequest, "invalid")
	// Operators cannot upload.
	requireErr(t, e.upload(path, e.opTok, nil, "a.txt", []byte("x")), http.StatusForbidden, "forbidden")

	// JSON pasted text.
	w = e.do(http.MethodPost, path, e.adminTok, map[string]any{"filename": "FAQ", "text": "  Асуулт хариулт  "})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	d = decode[docResp](t, w).Document
	assert.Equal(t, "FAQ", d.Filename)
	assert.Equal(t, "Асуулт хариулт", string(e.kn.uploaded(d.ID)))
	w = e.do(http.MethodPost, path, e.adminTok, map[string]any{"text": "hello"})
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, "pasted-text.txt", decode[docResp](t, w).Document.Filename)
	requireErr(t, e.do(http.MethodPost, path, e.adminTok, map[string]any{"filename": "x", "text": "  "}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, path, e.adminTok, "{bad"), http.StatusBadRequest, "invalid")

	// Unknown base → 404.
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-bases/"+uuid.NewString()+"/documents", e.adminTok,
		map[string]any{"text": "x"}), http.StatusNotFound, "not_found")
}

func TestKnowledgeDocumentTooLarge(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "KB")
	path := "/api/knowledge-bases/" + kb.ID.String() + "/documents"
	big := bytes.Repeat([]byte("a"), maxKnowledgeUpload+1)
	w := e.upload(path, e.adminTok, nil, "big.txt", big)
	requireErr(t, w, http.StatusBadRequest, "invalid")
	assert.Contains(t, decode[errBody](t, w).Error.Message, "20 MB")

	// Exactly 20 MB is accepted.
	w = e.upload(path, e.adminTok, nil, "ok.txt", big[:maxKnowledgeUpload])
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	// Way over the limit: the body cap trips while parsing.
	huge := bytes.Repeat([]byte("a"), maxKnowledgeUpload+2*knowledgeUploadSlack)
	requireErr(t, e.upload(path, e.adminTok, nil, "huge.txt", huge), http.StatusBadRequest, "invalid")
	w = e.do(http.MethodPost, path, e.adminTok, map[string]any{"text": string(huge)})
	requireErr(t, w, http.StatusBadRequest, "invalid")
}

func TestKnowledgeDocumentsListGetDeleteReprocess(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "KB")
	doc := e.seedDoc(kb, "a.txt", 60)
	e.seedDoc(kb, "b.txt", 0)
	other := e.seedDoc(e.seedKB(e.org.ID, "Other"), "c.txt", 1)

	w := e.do(http.MethodGet, "/api/knowledge-bases/"+kb.ID.String()+"/documents", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	lst := decode[list[domain.KnowledgeDocument]](t, w)
	require.Len(t, lst.Items, 2)
	assert.NotContains(t, []uuid.UUID{lst.Items[0].ID, lst.Items[1].ID}, other.ID)

	// First page of 50 chunks, then the rest via ?offset=.
	w = e.do(http.MethodGet, "/api/knowledge-documents/"+doc.ID.String(), e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := decode[docResp](t, w)
	assert.Equal(t, doc.ID, got.Document.ID)
	require.Len(t, got.Chunks, 50)
	assert.Equal(t, 60, got.ChunkTotal)
	assert.Equal(t, 0, got.Chunks[0].Seq)
	assert.Equal(t, "chunk 0", got.Chunks[0].Content)
	assert.Equal(t, "H", got.Chunks[0].Heading)
	assert.NotContains(t, w.Body.String(), "embedding")
	w = e.do(http.MethodGet, "/api/knowledge-documents/"+doc.ID.String()+"?offset=50", e.opTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got = decode[docResp](t, w)
	require.Len(t, got.Chunks, 10)
	assert.Equal(t, 50, got.Chunks[0].Seq)
	requireErr(t, e.do(http.MethodGet, "/api/knowledge-documents/"+doc.ID.String()+"?offset=-1", e.opTok, nil), http.StatusBadRequest, "invalid")

	// Reprocess (admin) → 202.
	requireErr(t, e.do(http.MethodPost, "/api/knowledge-documents/"+doc.ID.String()+"/reprocess", e.opTok, nil), http.StatusForbidden, "forbidden")
	w = e.do(http.MethodPost, "/api/knowledge-documents/"+doc.ID.String()+"/reprocess", e.adminTok, nil)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, domain.DocumentProcessing, decode[docResp](t, w).Document.Status)
	assert.Equal(t, []uuid.UUID{doc.ID}, e.kn.reprocessed)

	// Delete.
	requireErr(t, e.do(http.MethodDelete, "/api/knowledge-documents/"+doc.ID.String(), e.opTok, nil), http.StatusForbidden, "forbidden")
	w = e.do(http.MethodDelete, "/api/knowledge-documents/"+doc.ID.String(), e.adminTok, nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	requireErr(t, e.do(http.MethodGet, "/api/knowledge-documents/"+doc.ID.String(), e.adminTok, nil), http.StatusNotFound, "not_found")
}

func TestKnowledgeChunkPreviewWithoutLister(t *testing.T) {
	e := newEnv(t, func(d *Deps, _ *Config) { d.KnowledgeRepo = repoOnly{d.KnowledgeRepo} })
	kb := e.seedKB(e.org.ID, "KB")
	doc := e.seedDoc(kb, "a.txt", 3)
	w := e.do(http.MethodGet, "/api/knowledge-documents/"+doc.ID.String(), e.adminTok, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := decode[docResp](t, w)
	assert.NotNil(t, got.Chunks)
	assert.Empty(t, got.Chunks)
	assert.Contains(t, w.Body.String(), `"chunks":[]`)
}

// repoOnly hides the ChunkLister method of the fake.
type repoOnly struct{ domain.KnowledgeRepository }

func TestKnowledgeSearch(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "KB")
	path := "/api/knowledge-bases/" + kb.ID.String() + "/search"
	hit := domain.KnowledgeHit{ChunkID: uuid.New(), DocumentID: uuid.New(), Filename: "a.txt", Heading: "Үнэ", Content: "10000₮", Score: 0.9}
	e.kn.hits = []domain.KnowledgeHit{hit}

	w := e.do(http.MethodPost, path, e.opTok, map[string]any{"query": "  үнэ хэд вэ  "})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	resp := decode[struct {
		Hits      []domain.KnowledgeHit `json:"hits"`
		LatencyMs *int64                `json:"latencyMs"`
		Mode      string                `json:"mode"`
	}](t, w)
	require.Len(t, resp.Hits, 1)
	assert.Equal(t, hit, resp.Hits[0])
	require.NotNil(t, resp.LatencyMs)
	assert.GreaterOrEqual(t, *resp.LatencyMs, int64(0))
	assert.Equal(t, "hybrid", resp.Mode)
	assert.Equal(t, "үнэ хэд вэ", e.kn.lastQuery)
	assert.Equal(t, 5, e.kn.lastK)

	for in, want := range map[int]int{1: 1, 7: 7, 20: 20, 21: 20, 500: 20, -3: 1} {
		w = e.do(http.MethodPost, path, e.opTok, map[string]any{"query": "q", "k": in})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, want, e.kn.lastK, "k=%d", in)
	}

	// Empty result → [] and text mode passes through.
	e.kn.hits, e.kn.mode = nil, "text"
	w = e.do(http.MethodPost, path, e.opTok, map[string]any{"query": "q"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"hits":[]`)
	assert.Contains(t, w.Body.String(), `"mode":"text"`)

	requireErr(t, e.do(http.MethodPost, path, e.opTok, map[string]any{"query": "   "}), http.StatusBadRequest, "invalid")
	requireErr(t, e.do(http.MethodPost, path, e.opTok, map[string]any{}), http.StatusBadRequest, "invalid")
}

func TestAgentKnowledgeSearch(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "KB")
	e.kn.hits = []domain.KnowledgeHit{{ChunkID: uuid.New(), DocumentID: uuid.New(), Content: "answer", Score: 1}}
	path := "/internal/agent/knowledge/search"

	// Auth: no token / user JWT → 401.
	requireErr(t, e.do(http.MethodPost, path, "", map[string]any{"knowledgeBaseId": kb.ID, "query": "q"}), http.StatusUnauthorized, "unauthorized")
	requireErr(t, e.do(http.MethodPost, path, e.adminTok, map[string]any{"knowledgeBaseId": kb.ID, "query": "q"}), http.StatusUnauthorized, "unauthorized")
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"knowledgeBaseId":"`+kb.ID.String()+`","query":"q"}`))
	r.Header.Set(auth.AgentTokenHeader, "wrong")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	requireErr(t, w, http.StatusUnauthorized, "unauthorized")

	w = e.agent(http.MethodPost, path, map[string]any{"knowledgeBaseId": kb.ID, "query": "q", "k": 3})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	resp := decode[struct {
		Hits []domain.KnowledgeHit `json:"hits"`
	}](t, w)
	require.Len(t, resp.Hits, 1)
	assert.Equal(t, "answer", resp.Hits[0].Content)
	assert.Equal(t, 3, e.kn.lastK)
	assert.Equal(t, kb.ID, e.kn.lastKB)

	requireErr(t, e.agent(http.MethodPost, path, map[string]any{"query": "q"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.agent(http.MethodPost, path, map[string]any{"knowledgeBaseId": "bad", "query": "q"}), http.StatusBadRequest, "invalid")
	requireErr(t, e.agent(http.MethodPost, path, map[string]any{"knowledgeBaseId": kb.ID, "query": ""}), http.StatusBadRequest, "invalid")
	requireErr(t, e.agent(http.MethodPost, path, map[string]any{"knowledgeBaseId": uuid.New(), "query": "q"}), http.StatusNotFound, "not_found")
}

func TestProfileKnowledgeValidation(t *testing.T) {
	e := newEnv(t)
	kb := e.seedKB(e.org.ID, "KB")
	foreign := e.seedKB(e.org2.ID, "Foreign")
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "Support", "tools": []string{"end_call"}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	// Default mode is off.
	w := e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, base(nil))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	p := decode[struct {
		Profile domain.AgentProfile `json:"profile"`
	}](t, w).Profile
	assert.Nil(t, p.KnowledgeBaseID)
	assert.Equal(t, domain.KnowledgeOff, p.KnowledgeMode)

	// Valid base + mode.
	w = e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, base(map[string]any{"knowledgeBaseId": kb.ID, "knowledgeMode": "tool"}))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	p = decode[struct {
		Profile domain.AgentProfile `json:"profile"`
	}](t, w).Profile
	require.NotNil(t, p.KnowledgeBaseID)
	assert.Equal(t, kb.ID, *p.KnowledgeBaseID)
	assert.Equal(t, domain.KnowledgeTool, p.KnowledgeMode)
	stored, err := e.db.GetAgentProfile(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.KnowledgeTool, stored.KnowledgeMode)

	// Invalid: other org's base, unknown base, bad uuid, bad mode.
	for _, extra := range []map[string]any{
		{"knowledgeBaseId": foreign.ID, "knowledgeMode": "tool"},
		{"knowledgeBaseId": uuid.New(), "knowledgeMode": "context"},
		{"knowledgeBaseId": "nope", "knowledgeMode": "tool"},
		{"knowledgeBaseId": kb.ID, "knowledgeMode": "always"},
	} {
		requireErr(t, e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, base(extra)), http.StatusBadRequest, "invalid")
	}

	// PUT switches to context, then unlinks (mode without base → off).
	path := "/api/agent-profiles/" + p.ID.String()
	w = e.do(http.MethodPut, path, e.adminTok, base(map[string]any{"knowledgeBaseId": kb.ID.String(), "knowledgeMode": "context"}))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	p = decode[struct {
		Profile domain.AgentProfile `json:"profile"`
	}](t, w).Profile
	assert.Equal(t, domain.KnowledgeContext, p.KnowledgeMode)
	requireErr(t, e.do(http.MethodPut, path, e.adminTok, base(map[string]any{"knowledgeBaseId": foreign.ID.String(), "knowledgeMode": "tool"})),
		http.StatusBadRequest, "invalid")
	w = e.do(http.MethodPut, path, e.adminTok, base(map[string]any{"knowledgeBaseId": "", "knowledgeMode": "tool"}))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	p = decode[struct {
		Profile domain.AgentProfile `json:"profile"`
	}](t, w).Profile
	assert.Nil(t, p.KnowledgeBaseID)
	assert.Equal(t, domain.KnowledgeOff, p.KnowledgeMode)
	assert.NotContains(t, w.Body.String(), "knowledgeBaseId")
	assert.Contains(t, w.Body.String(), `"knowledgeMode":"off"`)
}

func TestProfileKnowledgeNotConfigured(t *testing.T) {
	e := newEnv(t, func(d *Deps, _ *Config) { d.Knowledge, d.KnowledgeRepo = nil, nil })
	w := e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, map[string]any{"name": "S", "knowledgeBaseId": uuid.New(), "knowledgeMode": "tool"})
	requireErr(t, w, http.StatusBadRequest, "invalid")
	w = e.do(http.MethodPost, "/api/agent-profiles", e.adminTok, map[string]any{"name": "S"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

type knowledgeBootstrap struct {
	Profile   domain.AgentProfile `json:"profile"`
	Knowledge *struct {
		ID          uuid.UUID            `json:"id"`
		Name        string               `json:"name"`
		Mode        domain.KnowledgeMode `json:"mode"`
		ContextText *string              `json:"contextText"`
		Truncated   bool                 `json:"truncated"`
	} `json:"knowledge"`
}

func (e *env) bootstrapWithProfile(kbID *uuid.UUID, mode domain.KnowledgeMode) *httptest.ResponseRecorder {
	e.t.Helper()
	p := domain.AgentProfile{ID: uuid.New(), OrgID: e.org.ID, Name: "KB agent", Language: "mn", Tools: []string{"end_call"},
		KnowledgeBaseID: kbID, KnowledgeMode: mode}
	require.NoError(e.t, e.db.CreateAgentProfile(context.Background(), &p))
	number := fmt.Sprintf("+9767700%04d", len(e.db.numbers)+1)
	e.seedNumber(e.org.ID, number, &p.ID)
	room := "call-" + uuid.NewString()
	return e.agent(http.MethodGet, "/internal/agent/bootstrap?room="+room+"&sipNumber="+urlEscape(number)+
		"&from=%2B97699112233&to="+urlEscape(number)+"&direction=inbound", nil)
}

func urlEscape(s string) string { return "%2B" + s[1:] }

func TestBootstrapKnowledge(t *testing.T) {
	t.Run("tool mode", func(t *testing.T) {
		e := newEnv(t)
		kb := e.seedKB(e.org.ID, "Manual")
		e.kn.contextText = "should not be sent"
		w := e.bootstrapWithProfile(&kb.ID, domain.KnowledgeTool)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		b := decode[knowledgeBootstrap](t, w)
		require.NotNil(t, b.Knowledge, w.Body.String())
		assert.Equal(t, kb.ID, b.Knowledge.ID)
		assert.Equal(t, "Manual", b.Knowledge.Name)
		assert.Equal(t, domain.KnowledgeTool, b.Knowledge.Mode)
		assert.Nil(t, b.Knowledge.ContextText)
		assert.False(t, b.Knowledge.Truncated)
		assert.Equal(t, domain.KnowledgeTool, b.Profile.KnowledgeMode)
	})

	t.Run("context mode", func(t *testing.T) {
		e := newEnv(t)
		kb := e.seedKB(e.org.ID, "Manual")
		e.kn.contextText, e.kn.truncated = "# Section\nfull text", true
		w := e.bootstrapWithProfile(&kb.ID, domain.KnowledgeContext)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		b := decode[knowledgeBootstrap](t, w)
		require.NotNil(t, b.Knowledge, w.Body.String())
		assert.Equal(t, domain.KnowledgeContext, b.Knowledge.Mode)
		require.NotNil(t, b.Knowledge.ContextText)
		assert.Equal(t, "# Section\nfull text", *b.Knowledge.ContextText)
		assert.True(t, b.Knowledge.Truncated)
	})

	nullCases := []struct {
		name   string
		mutate func(*Deps, *Config)
		setup  func(e *env) (*uuid.UUID, domain.KnowledgeMode)
	}{
		{"mode off", nil, func(e *env) (*uuid.UUID, domain.KnowledgeMode) {
			kb := e.seedKB(e.org.ID, "KB")
			return &kb.ID, domain.KnowledgeOff
		}},
		{"mode empty", nil, func(e *env) (*uuid.UUID, domain.KnowledgeMode) {
			kb := e.seedKB(e.org.ID, "KB")
			return &kb.ID, ""
		}},
		{"no base", nil, func(*env) (*uuid.UUID, domain.KnowledgeMode) { return nil, domain.KnowledgeTool }},
		{"unconfigured", func(d *Deps, _ *Config) { d.Knowledge, d.KnowledgeRepo = nil, nil }, func(*env) (*uuid.UUID, domain.KnowledgeMode) {
			id := uuid.New()
			return &id, domain.KnowledgeTool
		}},
		{"deleted base", nil, func(*env) (*uuid.UUID, domain.KnowledgeMode) {
			id := uuid.New()
			return &id, domain.KnowledgeContext
		}},
		{"foreign base", nil, func(e *env) (*uuid.UUID, domain.KnowledgeMode) {
			kb := e.seedKB(e.org2.ID, "Foreign")
			return &kb.ID, domain.KnowledgeTool
		}},
		{"service error", nil, func(e *env) (*uuid.UUID, domain.KnowledgeMode) {
			kb := e.seedKB(e.org.ID, "KB")
			e.kn.getBaseErr = errors.New("db down")
			return &kb.ID, domain.KnowledgeTool
		}},
		{"context error", nil, func(e *env) (*uuid.UUID, domain.KnowledgeMode) {
			kb := e.seedKB(e.org.ID, "KB")
			e.kn.contextErr = errors.New("db down")
			return &kb.ID, domain.KnowledgeContext
		}},
	}
	for _, tc := range nullCases {
		t.Run(tc.name, func(t *testing.T) {
			var mut []func(*Deps, *Config)
			if tc.mutate != nil {
				mut = append(mut, tc.mutate)
			}
			e := newEnv(t, mut...)
			id, mode := tc.setup(e)
			w := e.bootstrapWithProfile(id, mode)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"knowledge":null`)
			assert.Nil(t, decode[knowledgeBootstrap](t, w).Knowledge)
		})
	}
}
