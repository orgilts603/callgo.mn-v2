package crm

import (
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/knowledge"
)

// TestKnowledgeServiceOnStore runs ingestion and hybrid search end to end on
// PostgreSQL with the offline hash embedder.
func TestKnowledgeServiceOnStore(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "svc")
	svc := knowledge.NewService(s, s, nil, knowledge.Options{FakeEmbeddings: true}, zerolog.Nop())
	t.Cleanup(svc.Close)

	kb := &domain.KnowledgeBase{OrgID: org.ID, Name: "Гарын авлага", ChunkSize: 300, ChunkOverlap: 60}
	require.NoError(t, svc.CreateBase(ctx, kb))
	var body strings.Builder
	body.WriteString("# Хүргэлт\n\n")
	for i := 0; i < 8; i++ {
		body.WriteString("Хүргэлт Улаанбаатар хотод 24 цагийн дотор үнэгүй хийгдэнэ. Орон нутагт 3-5 хоног болно.\n\n")
	}
	body.WriteString("# Төлбөр\n\nТөлбөрийг QPay болон картаар төлнө.\n")
	d, err := svc.AddText(ctx, kb.ID, "manual.md", body.String())
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		got, err := s.GetDocument(ctx, d.ID)
		return err == nil && got.Status != domain.DocumentProcessing
	}, 10*time.Second, 20*time.Millisecond)
	got, err := s.GetDocument(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, domain.DocumentReady, got.Status, got.Error)
	require.Greater(t, got.ChunkCount, 2)

	base, err := svc.GetBase(ctx, kb.ID)
	require.NoError(t, err)
	require.Equal(t, knowledge.HashDims, base.EmbeddingDims)
	require.Equal(t, got.ChunkCount, base.ChunkCount)

	hits, mode, err := svc.Search(ctx, kb.ID, "QPay-ээр төлж болох уу", 3)
	require.NoError(t, err)
	require.Equal(t, knowledge.ModeHybrid, mode)
	require.NotEmpty(t, hits)
	require.Equal(t, "Төлбөр", hits[0].Heading)
	require.Equal(t, "manual.md", hits[0].Filename)

	text, truncated, err := svc.ContextText(ctx, kb.ID)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, 1, strings.Count(text, "## Хүргэлт"))
	require.Contains(t, text, "## Төлбөр\nТөлбөрийг QPay")

	// Reprocess re-chunks from the stored extracted text.
	require.NoError(t, svc.Reprocess(ctx, d.ID))
	require.Eventually(t, func() bool {
		got, err := s.GetDocument(ctx, d.ID)
		return err == nil && got.Status == domain.DocumentReady
	}, 10*time.Second, 20*time.Millisecond)
}
