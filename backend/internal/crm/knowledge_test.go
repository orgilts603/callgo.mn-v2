package crm

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func newKB(t *testing.T, ctx context.Context, s *Store, orgID uuid.UUID, name string) *domain.KnowledgeBase {
	t.Helper()
	kb := &domain.KnowledgeBase{OrgID: orgID, Name: name, EmbeddingModel: "test-embed"}
	require.NoError(t, s.CreateKnowledgeBase(ctx, kb))
	return kb
}

func newDoc(t *testing.T, ctx context.Context, s *Store, kbID uuid.UUID, name string) *domain.KnowledgeDocument {
	t.Helper()
	d := &domain.KnowledgeDocument{KnowledgeBaseID: kbID, Filename: name, MimeType: "text/plain"}
	require.NoError(t, s.CreateDocument(ctx, d))
	return d
}

func TestKnowledgeBaseCRUD(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "kb")
	other := newOrg(t, ctx, s, "kb2")
	cfg := &domain.LLMConfig{OrgID: org.ID, Name: "oa", Provider: domain.ProviderOpenAI, Model: "gpt-4o-mini",
		APIKey: "sk-test"}
	require.NoError(t, s.CreateLLMConfig(ctx, cfg))

	kb := &domain.KnowledgeBase{OrgID: org.ID, Name: "Manual", Description: "d", EmbeddingLLMConfigID: &cfg.ID,
		EmbeddingModel: "text-embedding-3-small"}
	require.NoError(t, s.CreateKnowledgeBase(ctx, kb))
	require.NotEqual(t, uuid.Nil, kb.ID)
	require.Equal(t, DefaultChunkSize, kb.ChunkSize)
	require.Equal(t, DefaultChunkOverlap, kb.ChunkOverlap)
	require.Zero(t, kb.EmbeddingDims)
	require.False(t, kb.CreatedAt.IsZero())
	newKB(t, ctx, s, org.ID, "Second")
	newKB(t, ctx, s, other.ID, "Other")

	requireErrIs(t, s.CreateKnowledgeBase(ctx, &domain.KnowledgeBase{OrgID: org.ID, Name: "bad", ChunkSize: 50}),
		domain.ErrInvalid)

	list, err := s.ListKnowledgeBases(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "Second", list[0].Name, "newest first")

	d1 := newDoc(t, ctx, s, kb.ID, "a.txt")
	require.Equal(t, org.ID, d1.OrgID, "org defaults to the base's")
	require.Equal(t, domain.DocumentProcessing, d1.Status)
	newDoc(t, ctx, s, kb.ID, "b.txt")
	requireErrIs(t, s.CreateDocument(ctx, &domain.KnowledgeDocument{KnowledgeBaseID: uuid.New(), Filename: "x"}),
		domain.ErrNotFound)
	require.NoError(t, s.ReplaceChunks(ctx, d1.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "one"}, {Seq: 1, Content: "two"},
	}))

	got, err := s.GetKnowledgeBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, 2, got.DocumentCount)
	require.Equal(t, 2, got.ChunkCount)
	require.Equal(t, cfg.ID, *got.EmbeddingLLMConfigID)
	require.Zero(t, got.EmbeddingDims, "text-only chunks do not fix dims")

	// Text-only chunks do not lock the embedding settings.
	got.Name = "Manual v2"
	got.EmbeddingModel = "text-embedding-3-large"
	got.ChunkSize = 800
	require.NoError(t, s.UpdateKnowledgeBase(ctx, got))
	require.Equal(t, "Manual v2", got.Name)
	require.Equal(t, 800, got.ChunkSize)
	require.Equal(t, 2, got.ChunkCount)

	// Once vectors exist the embedding settings are locked, the rest is not.
	require.NoError(t, s.ReplaceChunks(ctx, d1.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "one", Embedding: []float32{1, 0, 0}},
	}))
	got, err = s.GetKnowledgeBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, 3, got.EmbeddingDims)
	got.EmbeddingModel = "other"
	requireErrIs(t, s.UpdateKnowledgeBase(ctx, got), domain.ErrConflict)
	got.EmbeddingModel = "text-embedding-3-large"
	got.EmbeddingLLMConfigID = nil
	requireErrIs(t, s.UpdateKnowledgeBase(ctx, got), domain.ErrConflict)
	got.EmbeddingLLMConfigID = &cfg.ID
	got.Description = "new"
	require.NoError(t, s.UpdateKnowledgeBase(ctx, got))
	require.Equal(t, "new", got.Description)
	require.Equal(t, 3, got.EmbeddingDims)
	requireErrIs(t, s.UpdateKnowledgeBase(ctx, &domain.KnowledgeBase{ID: uuid.New(), Name: "x", ChunkSize: 1000}),
		domain.ErrNotFound)

	// Deleting the embedding config keeps the base (SET NULL).
	require.NoError(t, s.DeleteLLMConfig(ctx, cfg.ID))
	got, err = s.GetKnowledgeBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Nil(t, got.EmbeddingLLMConfigID)

	// Documents.
	d1.Status = domain.DocumentFailed
	d1.Error = "boom"
	d1.ChunkCount, d1.CharCount, d1.SizeBytes = 1, 3, 42
	require.NoError(t, s.UpdateDocument(ctx, d1))
	gd, err := s.GetDocument(ctx, d1.ID)
	require.NoError(t, err)
	require.Equal(t, domain.DocumentFailed, gd.Status)
	require.Equal(t, "boom", gd.Error)
	require.Equal(t, int64(42), gd.SizeBytes)
	require.Equal(t, kb.ID, gd.KnowledgeBaseID)
	d1.Status = "weird"
	requireErrIs(t, s.UpdateDocument(ctx, d1), domain.ErrInvalid)
	requireErrIs(t, s.UpdateDocument(ctx, &domain.KnowledgeDocument{ID: uuid.New(), Status: domain.DocumentReady}),
		domain.ErrNotFound)

	text, err := s.DocumentText(ctx, d1.ID)
	require.NoError(t, err)
	require.Empty(t, text)
	require.NoError(t, s.SetDocumentText(ctx, d1.ID, "Сайн байна уу"))
	text, err = s.DocumentText(ctx, d1.ID)
	require.NoError(t, err)
	require.Equal(t, "Сайн байна уу", text)
	requireErrIs(t, s.SetDocumentText(ctx, uuid.New(), "x"), domain.ErrNotFound)
	_, err = s.DocumentText(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)

	docs, err := s.ListDocuments(ctx, kb.ID)
	require.NoError(t, err)
	require.Len(t, docs, 2)
	require.Equal(t, "a.txt", docs[0].Filename)

	// Agent profile link; deleting the base unlinks it and cascades.
	p := &domain.AgentProfile{OrgID: org.ID, Name: "P", KnowledgeBaseID: &kb.ID, KnowledgeMode: domain.KnowledgeTool}
	require.NoError(t, s.CreateAgentProfile(ctx, p))
	require.NoError(t, s.DeleteDocument(ctx, docs[1].ID))
	requireErrIs(t, s.DeleteDocument(ctx, docs[1].ID), domain.ErrNotFound)
	require.NoError(t, s.DeleteKnowledgeBase(ctx, kb.ID))
	requireErrIs(t, s.DeleteKnowledgeBase(ctx, kb.ID), domain.ErrNotFound)
	_, err = s.GetKnowledgeBase(ctx, kb.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	_, err = s.GetDocument(ctx, d1.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	var n int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT count(*) FROM knowledge_chunks WHERE knowledge_base_id = $1`,
		kb.ID).Scan(&n))
	require.Zero(t, n, "chunks cascade")
	gp, err := s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Nil(t, gp.KnowledgeBaseID)
	require.Equal(t, domain.KnowledgeTool, gp.KnowledgeMode)

	// Deleting the organisation cascades to its bases.
	okb := newKB(t, ctx, s, other.ID, "gone")
	newDoc(t, ctx, s, okb.ID, "x.md")
	_, err = testPool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, other.ID)
	require.NoError(t, err)
	_, err = s.GetKnowledgeBase(ctx, okb.ID)
	requireErrIs(t, err, domain.ErrNotFound)
}

func TestAgentProfileKnowledgeFields(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "ap")
	kb := newKB(t, ctx, s, org.ID, "KB")

	p := &domain.AgentProfile{OrgID: org.ID, Name: "Default"}
	require.NoError(t, s.CreateAgentProfile(ctx, p))
	require.Equal(t, domain.KnowledgeOff, p.KnowledgeMode)
	got, err := s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, domain.KnowledgeOff, got.KnowledgeMode)
	require.Nil(t, got.KnowledgeBaseID)

	got.KnowledgeBaseID = &kb.ID
	got.KnowledgeMode = domain.KnowledgeContext
	require.NoError(t, s.UpdateAgentProfile(ctx, got))
	got, err = s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, kb.ID, *got.KnowledgeBaseID)
	require.Equal(t, domain.KnowledgeContext, got.KnowledgeMode)
	list, err := s.ListAgentProfiles(ctx, org.ID)
	require.NoError(t, err)
	require.Equal(t, kb.ID, *list[0].KnowledgeBaseID)

	got.KnowledgeMode = ""
	require.NoError(t, s.UpdateAgentProfile(ctx, got))
	require.Equal(t, domain.KnowledgeOff, got.KnowledgeMode)

	got.KnowledgeMode = "always"
	requireErrIs(t, s.UpdateAgentProfile(ctx, got), domain.ErrInvalid)
	missing := uuid.New()
	requireErrIs(t, s.CreateAgentProfile(ctx, &domain.AgentProfile{OrgID: org.ID, Name: "x", KnowledgeBaseID: &missing}),
		domain.ErrInvalid)
}

func TestKnowledgeChunksAndVectorSearch(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "vec")
	kb := newKB(t, ctx, s, org.ID, "Vec")
	other := newKB(t, ctx, s, org.ID, "Other")
	d := newDoc(t, ctx, s, kb.ID, "manual.md")
	d2 := newDoc(t, ctx, s, kb.ID, "faq.md")
	od := newDoc(t, ctx, s, other.ID, "other.md")

	chunks := []domain.KnowledgeChunk{
		{Seq: 0, Heading: "Хүргэлт", Content: "Хүргэлт 24 цагийн дотор.", Embedding: []float32{1, 0, 0}},
		{Seq: 1, Heading: "Үнэ", Content: "Үнэ 10000 төгрөг.", Embedding: []float32{0, 1, 0}},
		{Seq: 2, Heading: "Буцаалт", Content: "Буцаалт 7 хоногт.", Embedding: []float32{0, 0, 1}},
	}
	require.NoError(t, s.ReplaceChunks(ctx, d.ID, chunks))
	for _, c := range chunks {
		require.NotEqual(t, uuid.Nil, c.ID)
		require.Equal(t, kb.ID, c.KnowledgeBaseID)
		require.Equal(t, d.ID, c.DocumentID)
	}
	require.NoError(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "FAQ", Embedding: []float32{0.7, 0.7, 0}},
	}))
	require.NoError(t, s.ReplaceChunks(ctx, od.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "elsewhere", Embedding: []float32{1, 0, 0}},
	}))

	hits, err := s.SearchVector(ctx, kb.ID, []float32{0.9, 0.1, 0}, 2)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, chunks[0].ID, hits[0].ChunkID)
	require.Equal(t, "manual.md", hits[0].Filename)
	require.Equal(t, "Хүргэлт", hits[0].Heading)
	require.Equal(t, "FAQ", hits[1].Content)
	require.InDelta(t, 0.9939, hits[0].Score, 0.001)
	require.Greater(t, hits[0].Score, hits[1].Score)

	hits, err = s.SearchVector(ctx, kb.ID, []float32{0, 0, 1}, 10)
	require.NoError(t, err)
	require.Len(t, hits, 4, "only this base")
	require.Equal(t, chunks[2].ID, hits[0].ChunkID)
	require.InDelta(t, 1.0, hits[0].Score, 1e-5)

	// Query of another dimension matches nothing instead of erroring.
	hits, err = s.SearchVector(ctx, kb.ID, []float32{1, 0}, 3)
	require.NoError(t, err)
	require.Empty(t, hits)
	_, err = s.SearchVector(ctx, kb.ID, nil, 3)
	requireErrIs(t, err, domain.ErrInvalid)

	// Replace is a full swap; seq pages via ListChunks.
	require.NoError(t, s.ReplaceChunks(ctx, d.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "a", Embedding: []float32{1, 1, 0}},
		{Seq: 1, Content: "b", Embedding: []float32{1, 1, 1}},
	}))
	page, total, err := s.ListChunks(ctx, d.ID, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, page, 1)
	require.Equal(t, "b", page[0].Content)
	require.Nil(t, page[0].Embedding)
	page, total, err = s.ListChunks(ctx, uuid.New(), 0, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, page)
	require.NoError(t, s.ReplaceChunks(ctx, d.ID, nil))
	_, total, err = s.ListChunks(ctx, d.ID, 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	requireErrIs(t, s.ReplaceChunks(ctx, uuid.New(), nil), domain.ErrNotFound)
}

func TestKnowledgeDimsMismatch(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "dims")
	kb := newKB(t, ctx, s, org.ID, "Dims")
	d1 := newDoc(t, ctx, s, kb.ID, "a")
	d2 := newDoc(t, ctx, s, kb.ID, "b")

	require.NoError(t, s.ReplaceChunks(ctx, d1.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "x", Embedding: []float32{1, 0, 0}},
	}))
	got, err := s.GetKnowledgeBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, 3, got.EmbeddingDims)

	requireErrIs(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "y", Embedding: []float32{1, 0}},
	}), domain.ErrInvalid)
	// Inconsistent input is rejected before touching the database.
	requireErrIs(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "y", Embedding: []float32{1, 0, 0}}, {Seq: 1, Content: "z", Embedding: []float32{1, 0}},
	}), domain.ErrInvalid)
	requireErrIs(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "y", Embedding: []float32{1, 0, 0}}, {Seq: 1, Content: "z"},
	}), domain.ErrInvalid)
	requireErrIs(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "y", Embedding: []float32{}},
	}), domain.ErrInvalid)

	// The failed replace rolled back: d1's chunk survives.
	_, total, err := s.ListChunks(ctx, d1.ID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)

	// Replacing the only embedded document may change the dimension.
	require.NoError(t, s.ReplaceChunks(ctx, d1.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "x", Embedding: []float32{1, 0}},
	}))
	got, err = s.GetKnowledgeBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, 2, got.EmbeddingDims)
}

func TestKnowledgeTextSearchAndContext(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "txt")
	kb := newKB(t, ctx, s, org.ID, "Txt")
	d := newDoc(t, ctx, s, kb.ID, "guide.md")
	d2 := newDoc(t, ctx, s, kb.ID, "extra.md")

	overlap := "захиалгыг баталгаажуулна."
	require.NoError(t, s.ReplaceChunks(ctx, d.ID, []domain.KnowledgeChunk{
		{Seq: 0, Heading: "Захиалга", Content: "Та манай вэбсайтаар захиалга өгч болно. Оператор " + overlap},
		{Seq: 1, Heading: "Захиалга", Content: "Оператор " + overlap + " Төлбөрийг QPay-ээр төлнө."},
		{Seq: 2, Heading: "Хүргэлт", Content: "Хүргэлт Улаанбаатар хотод 24 цагийн дотор үнэгүй."},
	}))
	require.NoError(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "Delivery to the countryside takes 3-5 days."},
	}))

	// websearch AND match.
	hits, err := s.SearchText(ctx, kb.ID, "хүргэлт цагийн", 5)
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, "Хүргэлт", hits[0].Heading)
	require.Greater(t, hits[0].Score, float32(0))

	// Inflected Cyrillic form (захиалгын) reaches "захиалга"/"захиалгыг" via prefix.
	hits, err = s.SearchText(ctx, kb.ID, "захиалгын талаар асуух", 5)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "Захиалга", hits[0].Heading)

	// Short substring query (inside a word) falls back to ILIKE.
	hits, err = s.SearchText(ctx, kb.ID, "QPay", 5)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	hits, err = s.SearchText(ctx, kb.ID, "zzqx", 5)
	require.NoError(t, err)
	require.Empty(t, hits)
	hits, err = s.SearchText(ctx, kb.ID, "countryside", 5)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "extra.md", hits[0].Filename)

	// k caps, no duplicates across passes.
	hits, err = s.SearchText(ctx, kb.ID, "захиалга оператор хүргэлт", 2)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.NotEqual(t, hits[0].ChunkID, hits[1].ChunkID)
	hits, err = s.SearchText(ctx, kb.ID, "   ", 5)
	require.NoError(t, err)
	require.Empty(t, hits)
	// Operators in the query do not break parsing.
	_, err = s.SearchText(ctx, kb.ID, `"ханш" -доллар & | ! :* ( )`, 5)
	require.NoError(t, err)

	text, truncated, err := s.AllChunksText(ctx, kb.ID, 10000)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, 1, strings.Count(text, "## Захиалга"), "heading printed once per run")
	require.Equal(t, 1, strings.Count(text, overlap), "overlap removed")
	require.True(t, strings.HasPrefix(text, "## Захиалга\nТа манай"))
	require.Contains(t, text, "## Хүргэлт\nХүргэлт Улаанбаатар")
	require.True(t, strings.HasSuffix(text, "3-5 days."), "documents in created_at order")

	short, truncated, err := s.AllChunksText(ctx, kb.ID, 60)
	require.NoError(t, err)
	require.True(t, truncated)
	require.LessOrEqual(t, utf8.RuneCountInString(short), 60)
	require.True(t, strings.HasPrefix(text, short))
	empty, truncated, err := s.AllChunksText(ctx, uuid.New(), 100)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Empty(t, empty)
}

func TestMigrationKnowledgeDownUp(t *testing.T) {
	ctx, s := setup(t)
	t.Cleanup(func() {
		require.NoError(t, Migrate(context.Background(), testDSN))
		testPool.Reset()
	})
	org := newOrg(t, ctx, s, "kmig")
	kb := newKB(t, ctx, s, org.ID, "KB")
	d := newDoc(t, ctx, s, kb.ID, "a.txt")
	require.NoError(t, s.SetDocumentText(ctx, d.ID, "hello"))
	p := &domain.AgentProfile{OrgID: org.ID, Name: "P", KnowledgeBaseID: &kb.ID, KnowledgeMode: domain.KnowledgeTool}
	require.NoError(t, s.CreateAgentProfile(ctx, p))

	step := func(v uint) {
		require.NoError(t, runMigrations(ctx, testDSN, func(m *migrate.Migrate) error { return m.Migrate(v) }))
		testPool.Reset()
	}
	step(3)
	require.False(t, columnExists(t, ctx, "knowledge_documents", "extracted_text"))
	require.True(t, columnExists(t, ctx, "knowledge_documents", "filename"))

	step(2)
	for _, tbl := range []string{"knowledge_bases", "knowledge_documents", "knowledge_chunks"} {
		var exists bool
		require.NoError(t, testPool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+tbl).Scan(&exists))
		require.Falsef(t, exists, "%s after down", tbl)
	}
	require.False(t, columnExists(t, ctx, "agent_profiles", "knowledge_mode"))
	require.False(t, columnExists(t, ctx, "agent_profiles", "knowledge_base_id"))

	step(4)
	require.True(t, columnExists(t, ctx, "knowledge_documents", "extracted_text"))
	got, err := s.GetAgentProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, domain.KnowledgeOff, got.KnowledgeMode, "column default after re-up")
	require.Nil(t, got.KnowledgeBaseID)

	// Vectors still round-trip after the extension type was re-registered.
	kb2 := newKB(t, ctx, s, org.ID, "KB2")
	d2 := newDoc(t, ctx, s, kb2.ID, "b.txt")
	require.NoError(t, s.ReplaceChunks(ctx, d2.ID, []domain.KnowledgeChunk{
		{Seq: 0, Content: "v", Embedding: []float32{0.5, 0.5}},
	}))
	hits, err := s.SearchVector(ctx, kb2.ID, []float32{1, 1}, 1)
	require.NoError(t, err)
	require.Len(t, hits, 1)

	conn, err := testPool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	_, ok := conn.Conn().TypeMap().TypeForName("vector")
	require.True(t, ok, "vector codec registered by Open")
	var v []float32
	require.NoError(t, conn.QueryRow(ctx, `SELECT '[1,2.5,3]'::vector`).Scan(&v))
	require.Equal(t, []float32{1, 2.5, 3}, v)
	var null []float32
	require.NoError(t, conn.QueryRow(ctx, `SELECT NULL::vector`).Scan(&null))
	require.Nil(t, null)
	require.NoError(t, conn.QueryRow(ctx, `SELECT $1::vector`, []float32{4, 5}).Scan(&v))
	require.Equal(t, []float32{4, 5}, v)
}

func TestPrefixTermsAndOverlap(t *testing.T) {
	require.Equal(t, []string{"захиал:*", "цаг:*", "qpay:*"},
		prefixTerms("Захиалгын цаг? QPay, a"))
	require.Equal(t, []string{"хүрг:*"}, prefixTerms("хүргэлт хүргэх"))
	require.Empty(t, prefixTerms("& | ! :*"))

	prev := "alpha beta gamma delta epsilon zeta"
	cur := "delta epsilon zeta eta theta"
	require.Equal(t, len("delta epsilon zeta"), overlapLen(prev, cur))
	require.Zero(t, overlapLen("abc", "def ghi jkl mno pqr"))
	require.Equal(t, "hello", cutRunes("hello world", 7))
	require.Equal(t, "Сайн", cutRunes("Сайн байна", 6))
	require.Equal(t, "ab", cutRunes("ab", 5))
}
