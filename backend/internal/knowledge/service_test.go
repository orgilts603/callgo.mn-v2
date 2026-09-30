package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/embed"
)

const manual = `# Хүргэлт

Хүргэлт Улаанбаатар хотод 24 цагийн дотор үнэгүй хийгдэнэ. Орон нутагт 3-5 хоног.

# Төлбөр

Төлбөрийг QPay болон картаар төлнө. Бэлнээр төлөх боломжгүй.

# Буцаалт

Бараа буцаалт 7 хоногийн дотор баримттай хийгдэнэ.
`

type fixture struct {
	repo    *memRepo
	configs *memConfigs
	svc     *Service
	org     uuid.UUID
	stub    *stubEmbedder
	factory []string // "provider/model" per factory call
}

func newFixture(t *testing.T, opts Options) *fixture {
	t.Helper()
	f := &fixture{repo: newMemRepo(), configs: &memConfigs{}, org: uuid.New(), stub: &stubEmbedder{dims: 8}}
	factory := func(cfg *domain.LLMConfig, model string) (domain.Embedder, error) {
		f.factory = append(f.factory, string(cfg.Provider)+"/"+model)
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("%w: embed: openai needs an API key", domain.ErrInvalid)
		}
		return f.stub, nil
	}
	f.svc = NewService(f.repo, f.configs, factory, opts, zerolog.Nop())
	t.Cleanup(f.svc.Close)
	return f
}

// waitIdle waits until no ingestion is queued or running.
func (f *fixture) waitIdle(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		return len(f.svc.active) == 0
	}, 5*time.Second, 5*time.Millisecond)
}

func (f *fixture) doc(t *testing.T, id uuid.UUID) *domain.KnowledgeDocument {
	t.Helper()
	d, err := f.svc.GetDocument(context.Background(), id)
	require.NoError(t, err)
	return d
}

func TestIngestAndHybridSearchWithHashEmbeddings(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{FakeEmbeddings: true, Workers: 3})

	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "  Гарын авлага "}
	require.NoError(t, f.svc.CreateBase(ctx, kb))
	require.Equal(t, "Гарын авлага", kb.Name)
	require.Equal(t, embed.HashModel, kb.EmbeddingModel)
	require.Nil(t, kb.EmbeddingLLMConfigID)
	require.Equal(t, 1200, kb.ChunkSize)
	require.Equal(t, 200, kb.ChunkOverlap)

	d, err := f.svc.AddText(ctx, kb.ID, "", manual)
	require.NoError(t, err)
	require.Equal(t, domain.DocumentProcessing, d.Status)
	require.Equal(t, "text.txt", d.Filename)
	f.waitIdle(t)

	d = f.doc(t, d.ID)
	require.Equal(t, domain.DocumentReady, d.Status, d.Error)
	require.Equal(t, 3, d.ChunkCount)
	require.Greater(t, d.CharCount, 100)
	got, err := f.svc.GetBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, HashDims, got.EmbeddingDims)
	require.Equal(t, 3, got.ChunkCount)

	chunks, total, err := f.svc.ListChunks(ctx, d.ID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Equal(t, "Хүргэлт", chunks[0].Heading)
	require.Len(t, chunks[0].Embedding, HashDims)

	hits, mode, err := f.svc.Search(ctx, kb.ID, "Төлбөрийг QPay-ээр төлж болох уу?", 0)
	require.NoError(t, err)
	require.Equal(t, ModeHybrid, mode)
	require.NotEmpty(t, hits)
	require.LessOrEqual(t, len(hits), DefaultK)
	require.Equal(t, "Төлбөр", hits[0].Heading)
	require.Equal(t, "text.txt", hits[0].Filename)
	for i, h := range hits {
		require.GreaterOrEqual(t, h.Score, float32(0))
		require.LessOrEqual(t, h.Score, float32(1))
		if i > 0 {
			require.GreaterOrEqual(t, hits[i-1].Score, h.Score)
		}
	}
	require.InDelta(t, 1.0, hits[0].Score, 1e-6, "first in both lists")

	text, truncated, err := f.svc.ContextText(ctx, kb.ID)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Contains(t, text, "## Буцаалт")

	_, _, err = f.svc.Search(ctx, kb.ID, "  ", 5)
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, _, err = f.svc.Search(ctx, uuid.New(), "x", 5)
	require.ErrorIs(t, err, domain.ErrNotFound)
	_, _, err = f.svc.ContextText(ctx, uuid.New())
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestContextTextCap(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{ContextMaxChars: 80})
	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "KB"}
	require.NoError(t, f.svc.CreateBase(ctx, kb))
	_, err := f.svc.AddText(ctx, kb.ID, "m.md", manual)
	require.NoError(t, err)
	f.waitIdle(t)
	text, truncated, err := f.svc.ContextText(ctx, kb.ID)
	require.NoError(t, err)
	require.True(t, truncated)
	require.LessOrEqual(t, len([]rune(text)), 80)
}

func TestTextOnlyBase(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{})
	// The org's only config is Anthropic: no embeddings API → text-only.
	f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderAnthropic, APIKey: "k", IsDefault: true})
	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "Text"}
	require.NoError(t, f.svc.CreateBase(ctx, kb))
	require.Empty(t, kb.EmbeddingModel)
	require.Nil(t, kb.EmbeddingLLMConfigID)

	d, err := f.svc.AddText(ctx, kb.ID, "faq.md", manual)
	require.NoError(t, err)
	f.waitIdle(t)
	require.Equal(t, domain.DocumentReady, f.doc(t, d.ID).Status)
	chunks, _, err := f.svc.ListChunks(ctx, d.ID, 10, 0)
	require.NoError(t, err)
	require.Nil(t, chunks[0].Embedding)

	hits, mode, err := f.svc.Search(ctx, kb.ID, "буцаалт", 3)
	require.NoError(t, err)
	require.Equal(t, ModeText, mode)
	require.Len(t, hits, 1)
	require.Equal(t, "Буцаалт", hits[0].Heading)
	require.InDelta(t, 1.0, hits[0].Score, 1e-6)
	require.Empty(t, f.factory, "no embedder built")

	// Explicitly choosing the Anthropic config is invalid.
	anth := f.configs.list[0].ID
	err = f.svc.CreateBase(ctx, &domain.KnowledgeBase{OrgID: f.org, Name: "x", EmbeddingLLMConfigID: &anth})
	require.ErrorIs(t, err, domain.ErrInvalid)

	// Text-only chunks do not lock embedding settings; switching triggers a re-ingest with vectors.
	oa := f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderOpenAI, APIKey: "sk"})
	kb.EmbeddingLLMConfigID = &oa.ID
	kb.EmbeddingModel = ""
	require.NoError(t, f.svc.UpdateBase(ctx, kb))
	require.Equal(t, embed.DefaultOpenAIModel, kb.EmbeddingModel)
	f.waitIdle(t)
	chunks, _, err = f.svc.ListChunks(ctx, d.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, chunks[0].Embedding, 8)
	_, mode, err = f.svc.Search(ctx, kb.ID, "буцаалт", 3)
	require.NoError(t, err)
	require.Equal(t, ModeHybrid, mode)
}

func TestConfiguredEmbedder(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{FakeEmbeddings: true})
	other := f.configs.add(domain.LLMConfig{OrgID: uuid.New(), Provider: domain.ProviderOpenAI, APIKey: "sk"})
	cfg := f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderOpenAI, APIKey: "sk", IsDefault: true})
	compat := f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderOpenAICompatible, APIKey: "k"})

	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "Default cfg", ChunkSize: 400, ChunkOverlap: 50}
	require.NoError(t, f.svc.CreateBase(ctx, kb))
	require.Equal(t, cfg.ID, *kb.EmbeddingLLMConfigID)
	require.Equal(t, embed.DefaultOpenAIModel, kb.EmbeddingModel)

	for _, bad := range []*domain.KnowledgeBase{
		{OrgID: f.org, Name: ""},
		{OrgID: f.org, Name: "x", ChunkSize: 100},
		{OrgID: f.org, Name: "x", ChunkSize: 1000, ChunkOverlap: 600},
		{OrgID: f.org, Name: "x", EmbeddingLLMConfigID: &compat.ID},
		{OrgID: f.org, Name: "x", EmbeddingLLMConfigID: &other.ID},
		{Name: "x"},
	} {
		require.ErrorIs(t, f.svc.CreateBase(ctx, bad), domain.ErrInvalid, "%+v", bad)
	}
	kc := &domain.KnowledgeBase{OrgID: f.org, Name: "compat", EmbeddingLLMConfigID: &compat.ID, EmbeddingModel: "bge-m3"}
	require.NoError(t, f.svc.CreateBase(ctx, kc))

	long := strings.Repeat("Урт догол мөр энд байна. ", 60)
	d, err := f.svc.AddText(ctx, kb.ID, "long.txt", long)
	require.NoError(t, err)
	f.waitIdle(t)
	d = f.doc(t, d.ID)
	require.Equal(t, domain.DocumentReady, d.Status, d.Error)
	require.Greater(t, d.ChunkCount, 2)
	require.Equal(t, []string{"openai/text-embedding-3-small"}, f.factory)
	require.Equal(t, d.ChunkCount, f.stub.inputs)
	kbNow, err := f.svc.GetBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, 8, kbNow.EmbeddingDims)

	// Locked once embedded.
	kbNow.EmbeddingModel = "text-embedding-3-large"
	require.ErrorIs(t, f.svc.UpdateBase(ctx, kbNow), domain.ErrConflict)
	// Leaving embedding fields empty keeps them; chunking change re-ingests.
	upd := &domain.KnowledgeBase{ID: kb.ID, Name: "Renamed", ChunkSize: 800, ChunkOverlap: 100}
	require.NoError(t, f.svc.UpdateBase(ctx, upd))
	require.Equal(t, embed.DefaultOpenAIModel, upd.EmbeddingModel)
	require.Equal(t, cfg.ID, *upd.EmbeddingLLMConfigID)
	f.waitIdle(t)
	d2 := f.doc(t, d.ID)
	require.Equal(t, domain.DocumentReady, d2.Status)
	require.Less(t, d2.ChunkCount, d.ChunkCount, "re-chunked with the larger size")

	// Query embedding failure degrades to text search.
	f.stub.failQ = errors.New("rate limited")
	hits, mode, err := f.svc.Search(ctx, kb.ID, "догол мөр", 2)
	require.NoError(t, err)
	require.Equal(t, ModeText, mode)
	require.Len(t, hits, 2)
	f.stub.failQ = nil
	_, mode, err = f.svc.Search(ctx, kb.ID, "догол мөр", 2)
	require.NoError(t, err)
	require.Equal(t, ModeHybrid, mode)

	// Document embedding failure → failed with a readable message.
	f.stub.fail = &embed.APIError{Status: 401, Message: "Incorrect API key provided"}
	bad, err := f.svc.AddText(ctx, kb.ID, "b.txt", "Шинэ мэдээлэл энд байна.")
	require.NoError(t, err)
	f.waitIdle(t)
	bad = f.doc(t, bad.ID)
	require.Equal(t, domain.DocumentFailed, bad.Status)
	require.Equal(t, "Embedding failed: provider returned HTTP 401: Incorrect API key provided", bad.Error)
	f.stub.fail = nil

	// Reprocess from the stored text recovers it.
	require.NoError(t, f.svc.Reprocess(ctx, bad.ID))
	f.waitIdle(t)
	require.Equal(t, domain.DocumentReady, f.doc(t, bad.ID).Status)

	// A config that cannot build an embedder fails ingestion visibly.
	nokey := f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderOpenAI})
	kn := &domain.KnowledgeBase{OrgID: f.org, Name: "nokey", EmbeddingLLMConfigID: &nokey.ID}
	require.NoError(t, f.svc.CreateBase(ctx, kn))
	dn, err := f.svc.AddText(ctx, kn.ID, "n.txt", "Текст байна.")
	require.NoError(t, err)
	f.waitIdle(t)
	dn = f.doc(t, dn.ID)
	require.Equal(t, domain.DocumentFailed, dn.Status)
	require.Contains(t, dn.Error, "Embedding is not configured correctly")
	require.Contains(t, dn.Error, "needs an API key")
}

func buildDOCX(t *testing.T, paragraphs ...string) []byte {
	t.Helper()
	var body strings.Builder
	for _, p := range paragraphs {
		style := ""
		if h, ok := strings.CutPrefix(p, "# "); ok {
			style, p = `<w:pPr><w:pStyle w:val="Heading1"/></w:pPr>`, h
		}
		body.WriteString(`<w:p>` + style + `<w:r><w:t>` + p + `</w:t></w:r></w:p>`)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	require.NoError(t, err)
	_, err = w.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		body.String() + `</w:body></w:document>`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestAddDocumentFormats(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{FakeEmbeddings: true})
	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "Docs"}
	require.NoError(t, f.svc.CreateBase(ctx, kb))

	d, err := f.svc.AddDocument(ctx, kb.ID, `C:\Users\me\Гарын авлага.docx`, "",
		bytes.NewReader(buildDOCX(t, "# Нээлтийн цаг", "Бид өдөр бүр 09:00-18:00 цагт ажиллана.")))
	require.NoError(t, err)
	require.Equal(t, "Гарын авлага.docx", d.Filename)
	require.Equal(t, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", d.MimeType)
	require.Positive(t, d.SizeBytes)

	csvDoc, err := f.svc.AddDocument(ctx, kb.ID, "prices.csv", "text/csv", strings.NewReader("Бараа,Үнэ\nЦай,5000\n"))
	require.NoError(t, err)
	scan, err := f.svc.AddDocument(ctx, kb.ID, "scan.pdf", "application/pdf", strings.NewReader("%PDF-1.4 no xref"))
	require.NoError(t, err, "extraction problems surface on the document, not the request")
	f.waitIdle(t)

	d = f.doc(t, d.ID)
	require.Equal(t, domain.DocumentReady, d.Status, d.Error)
	chunks, _, err := f.svc.ListChunks(ctx, d.ID, 5, 0)
	require.NoError(t, err)
	require.Equal(t, "Нээлтийн цаг", chunks[0].Heading)
	require.Equal(t, domain.DocumentReady, f.doc(t, csvDoc.ID).Status)
	chunks, _, err = f.svc.ListChunks(ctx, csvDoc.ID, 5, 0)
	require.NoError(t, err)
	require.Equal(t, "Бараа: Цай; Үнэ: 5000", chunks[0].Content)
	scan = f.doc(t, scan.ID)
	require.Equal(t, domain.DocumentFailed, scan.Status)
	require.Contains(t, scan.Error, "PDF")

	// Rejected synchronously.
	_, err = f.svc.AddDocument(ctx, kb.ID, "deck.pptx", "", strings.NewReader("x"))
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.AddDocument(ctx, kb.ID, "empty.txt", "", strings.NewReader("  "))
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.AddDocument(ctx, kb.ID, "big.txt", "", bytes.NewReader(make([]byte, 20<<20+1)))
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = f.svc.AddDocument(ctx, uuid.New(), "a.txt", "", strings.NewReader("x"))
	require.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.AddText(ctx, kb.ID, "a", " \n ")
	require.ErrorIs(t, err, domain.ErrInvalid)

	// Reprocess of a document whose extraction failed has no text to reuse.
	require.ErrorIs(t, f.svc.Reprocess(ctx, scan.ID), domain.ErrConflict)
	require.ErrorIs(t, f.svc.Reprocess(ctx, uuid.New()), domain.ErrNotFound)

	docs, err := f.svc.ListDocuments(ctx, kb.ID)
	require.NoError(t, err)
	require.Len(t, docs, 3)
	require.NoError(t, f.svc.DeleteDocument(ctx, scan.ID))
	require.NoError(t, f.svc.DeleteBase(ctx, kb.ID))
	_, err = f.svc.GetBase(ctx, kb.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)
	list, err := f.svc.ListBases(ctx, f.org)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestDeleteDuringIngestAndClose(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{Workers: 1})
	cfg := f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderOpenAI, APIKey: "sk"})
	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "slow", EmbeddingLLMConfigID: &cfg.ID}
	require.NoError(t, f.svc.CreateBase(ctx, kb))
	f.stub.block = make(chan struct{})

	a, err := f.svc.AddText(ctx, kb.ID, "a.txt", "Эхний баримт.")
	require.NoError(t, err)
	b, err := f.svc.AddText(ctx, kb.ID, "b.txt", "Хоёр дахь баримт.")
	require.NoError(t, err)
	// Deleting a document mid-ingestion is harmless.
	require.NoError(t, f.svc.DeleteDocument(ctx, a.ID))

	f.svc.Close()
	f.svc.Close() // idempotent
	got := f.doc(t, b.ID)
	require.Equal(t, domain.DocumentFailed, got.Status)
	require.Contains(t, got.Error, "interrupted")
	_, err = f.svc.AddText(ctx, kb.ID, "c.txt", "x")
	require.Error(t, err)
}

func TestReprocessWhileActiveCoalesces(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, Options{Workers: 1})
	cfg := f.configs.add(domain.LLMConfig{OrgID: f.org, Provider: domain.ProviderOpenAI, APIKey: "sk"})
	kb := &domain.KnowledgeBase{OrgID: f.org, Name: "c", EmbeddingLLMConfigID: &cfg.ID}
	require.NoError(t, f.svc.CreateBase(ctx, kb))
	f.stub.block = make(chan struct{})
	d, err := f.svc.AddText(ctx, kb.ID, "a.txt", "Баримт нэг.")
	require.NoError(t, err)
	require.NoError(t, f.svc.Reprocess(ctx, d.ID), "no-op while queued")
	// A pending job for an active document replaces the earlier pending one.
	require.NoError(t, f.svc.enqueue(ctx, job{docID: d.ID}))
	require.NoError(t, f.svc.enqueue(ctx, job{docID: d.ID}))
	f.svc.mu.Lock()
	require.Len(t, f.svc.pending, 1)
	f.svc.mu.Unlock()
	close(f.stub.block)
	f.waitIdle(t)
	require.Equal(t, domain.DocumentReady, f.doc(t, d.ID).Status)
	require.Equal(t, 2, f.stub.calls, "initial ingest + one coalesced rerun")
}

func hit(id uuid.UUID) domain.KnowledgeHit {
	return domain.KnowledgeHit{ChunkID: id, Content: id.String()[:4]}
}

func TestFuseRRF(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	vec := []domain.KnowledgeHit{hit(a), hit(b), hit(c)}
	txt := []domain.KnowledgeHit{hit(c), hit(a), hit(d), hit(c)}

	got := fuseRRF(10, vec, txt)
	require.Len(t, got, 4, "deduplicated")
	ids := []uuid.UUID{got[0].ChunkID, got[1].ChunkID, got[2].ChunkID, got[3].ChunkID}
	// a: 1/61+1/62, c: 1/63+1/61, b: 1/62, d: 1/63.
	require.Equal(t, []uuid.UUID{a, c, b, d}, ids)
	best := 2.0 / 61
	want := []float64{(1.0/61 + 1.0/62) / best, (1.0/63 + 1.0/61) / best, (1.0 / 62) / best, (1.0 / 63) / best}
	for i, w := range want {
		require.InDelta(t, w, got[i].Score, 1e-6)
		require.GreaterOrEqual(t, got[i].Score, float32(0))
		require.LessOrEqual(t, got[i].Score, float32(1))
	}

	top := fuseRRF(2, vec, txt)
	require.Len(t, top, 2)
	require.Equal(t, a, top[0].ChunkID)

	single := fuseRRF(5, txt)
	require.Equal(t, c, single[0].ChunkID)
	require.InDelta(t, 1.0, single[0].Score, 1e-6)
	require.Len(t, single, 3)

	// Rank 1 in both lists is exactly 1; ties keep first-seen order.
	both := fuseRRF(5, []domain.KnowledgeHit{hit(a), hit(b)}, []domain.KnowledgeHit{hit(a), hit(c)})
	require.InDelta(t, 1.0, both[0].Score, 1e-6)
	require.Equal(t, []uuid.UUID{a, b, c}, []uuid.UUID{both[0].ChunkID, both[1].ChunkID, both[2].ChunkID})

	require.Empty(t, fuseRRF(5))
	require.Empty(t, fuseRRF(5, nil, nil))
}
