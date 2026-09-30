package crm

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Knowledge base defaults applied when a base is created with zero values.
const (
	DefaultChunkSize    = 1200
	DefaultChunkOverlap = 200
)

// knowledgeBaseCols selects a base (alias kb) with its document/chunk counts.
const knowledgeBaseCols = `kb.id, kb.org_id, kb.name, kb.description, kb.embedding_llm_config_id,
	kb.embedding_model, kb.embedding_dims, kb.chunk_size, kb.chunk_overlap,
	(SELECT count(*) FROM knowledge_documents d WHERE d.knowledge_base_id = kb.id),
	(SELECT count(*) FROM knowledge_chunks c WHERE c.knowledge_base_id = kb.id),
	kb.created_at, kb.updated_at`

const knowledgeDocumentCols = `id, knowledge_base_id, org_id, filename, mime_type, size_bytes, status, error,
	chunk_count, char_count, created_at, updated_at`

// knowledgeHitCols selects a hit from chunks c joined with documents d; the
// score expression is appended by each query.
const knowledgeHitCols = `c.id, c.document_id, d.filename, c.heading, c.content`

func scanKnowledgeBase(row pgx.Row) (*domain.KnowledgeBase, error) {
	var kb domain.KnowledgeBase
	err := row.Scan(&kb.ID, &kb.OrgID, &kb.Name, &kb.Description, &kb.EmbeddingLLMConfigID, &kb.EmbeddingModel,
		&kb.EmbeddingDims, &kb.ChunkSize, &kb.ChunkOverlap, &kb.DocumentCount, &kb.ChunkCount,
		&kb.CreatedAt, &kb.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &kb, nil
}

func scanKnowledgeDocument(row pgx.Row) (*domain.KnowledgeDocument, error) {
	var d domain.KnowledgeDocument
	err := row.Scan(&d.ID, &d.KnowledgeBaseID, &d.OrgID, &d.Filename, &d.MimeType, &d.SizeBytes, &d.Status,
		&d.Error, &d.ChunkCount, &d.CharCount, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func scanKnowledgeHit(row pgx.Row) (*domain.KnowledgeHit, error) {
	var h domain.KnowledgeHit
	var score float64
	if err := row.Scan(&h.ChunkID, &h.DocumentID, &h.Filename, &h.Heading, &h.Content, &score); err != nil {
		return nil, err
	}
	if math.IsNaN(score) || math.IsInf(score, 0) {
		score = 0
	}
	h.Score = float32(score)
	return &h, nil
}

// ---------------------------------------------------------------------------
// Knowledge bases
// ---------------------------------------------------------------------------

// CreateKnowledgeBase inserts a base. ChunkSize/ChunkOverlap default to
// 1200/200; EmbeddingDims starts at 0 and is fixed by the first stored chunk.
func (s *Store) CreateKnowledgeBase(ctx context.Context, kb *domain.KnowledgeBase) error {
	if kb.ChunkSize == 0 {
		kb.ChunkSize = DefaultChunkSize
	}
	if kb.ChunkOverlap == 0 && kb.ChunkSize > DefaultChunkOverlap {
		kb.ChunkOverlap = DefaultChunkOverlap
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO knowledge_bases (id, org_id, name, description, embedding_llm_config_id, embedding_model,
			chunk_size, chunk_overlap)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, embedding_dims, created_at, updated_at`,
		nilIfZero(kb.ID), kb.OrgID, kb.Name, kb.Description, kb.EmbeddingLLMConfigID, kb.EmbeddingModel,
		kb.ChunkSize, kb.ChunkOverlap)
	if err := row.Scan(&kb.ID, &kb.EmbeddingDims, &kb.CreatedAt, &kb.UpdatedAt); err != nil {
		return dbErr("create knowledge base", err)
	}
	kb.DocumentCount, kb.ChunkCount = 0, 0
	return nil
}

// UpdateKnowledgeBase overwrites name, description, embedding settings and
// chunking parameters, then refreshes kb from the database (counts, dims).
// Changing the embedding config or model once any chunk carries an embedding
// is a domain.ErrConflict; when no vectors are stored yet the change resets
// EmbeddingDims to 0.
func (s *Store) UpdateKnowledgeBase(ctx context.Context, kb *domain.KnowledgeBase) error {
	return s.inTx(ctx, func(tx *Store) error {
		var (
			curCfg   *uuid.UUID
			curModel string
		)
		err := tx.db.QueryRow(ctx,
			`SELECT embedding_llm_config_id, embedding_model FROM knowledge_bases WHERE id = $1 FOR UPDATE`,
			kb.ID).Scan(&curCfg, &curModel)
		if err != nil {
			return dbErr("update knowledge base", err)
		}
		changed := curModel != kb.EmbeddingModel || !sameUUIDPtr(curCfg, kb.EmbeddingLLMConfigID)
		if changed {
			var embedded bool
			if err := tx.db.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM knowledge_chunks WHERE knowledge_base_id = $1 AND embedding IS NOT NULL)`,
				kb.ID).Scan(&embedded); err != nil {
				return dbErr("update knowledge base", err)
			}
			if embedded {
				return fmt.Errorf("crm: update knowledge base: %w: embedding settings are locked once chunks are embedded",
					domain.ErrConflict)
			}
		}
		got, err := scanKnowledgeBase(tx.db.QueryRow(ctx,
			`UPDATE knowledge_bases AS kb SET name = $2, description = $3, embedding_llm_config_id = $4,
				embedding_model = $5, chunk_size = $6, chunk_overlap = $7,
				embedding_dims = CASE WHEN $8 THEN 0 ELSE embedding_dims END, updated_at = now()
			 WHERE kb.id = $1
			 RETURNING `+knowledgeBaseCols,
			kb.ID, kb.Name, kb.Description, kb.EmbeddingLLMConfigID, kb.EmbeddingModel, kb.ChunkSize, kb.ChunkOverlap,
			changed))
		if err != nil {
			return dbErr("update knowledge base", err)
		}
		*kb = *got
		return nil
	})
}

func sameUUIDPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// DeleteKnowledgeBase removes a base with its documents and chunks; agent
// profiles referencing it are unlinked (ON DELETE SET NULL).
func (s *Store) DeleteKnowledgeBase(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1`, id)
	return affected("delete knowledge base", tag, err)
}

// GetKnowledgeBase returns a base with its document and chunk counts.
func (s *Store) GetKnowledgeBase(ctx context.Context, id uuid.UUID) (*domain.KnowledgeBase, error) {
	kb, err := scanKnowledgeBase(s.db.QueryRow(ctx,
		`SELECT `+knowledgeBaseCols+` FROM knowledge_bases kb WHERE kb.id = $1`, id))
	return kb, dbErr("get knowledge base", err)
}

// ListKnowledgeBases lists an organisation's bases, newest first.
func (s *Store) ListKnowledgeBases(ctx context.Context, orgID uuid.UUID) ([]domain.KnowledgeBase, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+knowledgeBaseCols+` FROM knowledge_bases kb WHERE kb.org_id = $1
		 ORDER BY kb.created_at DESC, kb.name`, orgID)
	if err != nil {
		return nil, dbErr("list knowledge bases", err)
	}
	return collect("list knowledge bases", rows, scanKnowledgeBase)
}

// ---------------------------------------------------------------------------
// Documents
// ---------------------------------------------------------------------------

// CreateDocument inserts a document into its base. OrgID defaults to the
// base's organisation and Status to "processing"; ErrNotFound when the base
// does not exist.
func (s *Store) CreateDocument(ctx context.Context, d *domain.KnowledgeDocument) error {
	if d.Status == "" {
		d.Status = domain.DocumentProcessing
	}
	var orgID *uuid.UUID
	if d.OrgID != uuid.Nil {
		orgID = &d.OrgID
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO knowledge_documents (id, knowledge_base_id, org_id, filename, mime_type, size_bytes, status,
			error, chunk_count, char_count)
		 SELECT COALESCE($1, gen_random_uuid()), kb.id, COALESCE($3, kb.org_id), $4, $5, $6, $7, $8, $9, $10
		   FROM knowledge_bases kb WHERE kb.id = $2
		 RETURNING id, org_id, created_at, updated_at`,
		nilIfZero(d.ID), d.KnowledgeBaseID, orgID, d.Filename, d.MimeType, d.SizeBytes, d.Status, d.Error,
		d.ChunkCount, d.CharCount)
	return dbErr("create knowledge document", row.Scan(&d.ID, &d.OrgID, &d.CreatedAt, &d.UpdatedAt))
}

// UpdateDocument overwrites a document's mutable fields (filename, mime,
// size, status, error, counts).
func (s *Store) UpdateDocument(ctx context.Context, d *domain.KnowledgeDocument) error {
	row := s.db.QueryRow(ctx,
		`UPDATE knowledge_documents SET filename = $2, mime_type = $3, size_bytes = $4, status = $5, error = $6,
			chunk_count = $7, char_count = $8, updated_at = now()
		 WHERE id = $1
		 RETURNING knowledge_base_id, org_id, created_at, updated_at`,
		d.ID, d.Filename, d.MimeType, d.SizeBytes, d.Status, d.Error, d.ChunkCount, d.CharCount)
	return dbErr("update knowledge document",
		row.Scan(&d.KnowledgeBaseID, &d.OrgID, &d.CreatedAt, &d.UpdatedAt))
}

// DeleteDocument removes a document and its chunks.
func (s *Store) DeleteDocument(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM knowledge_documents WHERE id = $1`, id)
	return affected("delete knowledge document", tag, err)
}

// GetDocument returns a document by ID.
func (s *Store) GetDocument(ctx context.Context, id uuid.UUID) (*domain.KnowledgeDocument, error) {
	d, err := scanKnowledgeDocument(s.db.QueryRow(ctx,
		`SELECT `+knowledgeDocumentCols+` FROM knowledge_documents WHERE id = $1`, id))
	return d, dbErr("get knowledge document", err)
}

// ListDocuments lists a base's documents, oldest first.
func (s *Store) ListDocuments(ctx context.Context, kbID uuid.UUID) ([]domain.KnowledgeDocument, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+knowledgeDocumentCols+` FROM knowledge_documents WHERE knowledge_base_id = $1
		 ORDER BY created_at, id`, kbID)
	if err != nil {
		return nil, dbErr("list knowledge documents", err)
	}
	return collect("list knowledge documents", rows, scanKnowledgeDocument)
}

// SetDocumentText stores the document's extracted plain text so it can be
// re-chunked without the original upload.
func (s *Store) SetDocumentText(ctx context.Context, id uuid.UUID, text string) error {
	tag, err := s.db.Exec(ctx, `UPDATE knowledge_documents SET extracted_text = $2 WHERE id = $1`, id, text)
	return affected("set knowledge document text", tag, err)
}

// DocumentText returns the stored extracted text ("" when none was stored).
func (s *Store) DocumentText(ctx context.Context, id uuid.UUID) (string, error) {
	var text *string
	err := s.db.QueryRow(ctx, `SELECT extracted_text FROM knowledge_documents WHERE id = $1`, id).Scan(&text)
	if err != nil {
		return "", dbErr("get knowledge document text", err)
	}
	if text == nil {
		return "", nil
	}
	return *text, nil
}

// ---------------------------------------------------------------------------
// Chunks
// ---------------------------------------------------------------------------

// ReplaceChunks deletes the document's chunks and inserts the given ones in
// one transaction. Chunk KnowledgeBaseID/DocumentID are taken from the
// document. Embeddings must be all present with one dimension or all absent
// (text-only). The base's embedding_dims is set when it is 0 (or when no
// other embedded chunk remains); a different dimension from the base's is
// domain.ErrInvalid.
func (s *Store) ReplaceChunks(ctx context.Context, docID uuid.UUID, chunks []domain.KnowledgeChunk) error {
	dims, err := chunkDims(chunks)
	if err != nil {
		return fmt.Errorf("crm: replace chunks: %w", err)
	}
	return s.inTx(ctx, func(tx *Store) error {
		var kbID uuid.UUID
		if err := tx.db.QueryRow(ctx, `SELECT knowledge_base_id FROM knowledge_documents WHERE id = $1`, docID).
			Scan(&kbID); err != nil {
			return dbErr("replace chunks: get document", err)
		}
		// Lock the base so concurrent ingestions agree on its dimension.
		var baseDims int
		if err := tx.db.QueryRow(ctx, `SELECT embedding_dims FROM knowledge_bases WHERE id = $1 FOR UPDATE`, kbID).
			Scan(&baseDims); err != nil {
			return dbErr("replace chunks: lock base", err)
		}
		if _, err := tx.db.Exec(ctx, `DELETE FROM knowledge_chunks WHERE document_id = $1`, docID); err != nil {
			return dbErr("replace chunks: delete", err)
		}
		if dims > 0 && baseDims != dims {
			if baseDims != 0 {
				var others bool
				if err := tx.db.QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM knowledge_chunks WHERE knowledge_base_id = $1 AND embedding IS NOT NULL)`,
					kbID).Scan(&others); err != nil {
					return dbErr("replace chunks: check dims", err)
				}
				if others {
					return fmt.Errorf("crm: replace chunks: %w: embedding has %d dimensions, knowledge base uses %d",
						domain.ErrInvalid, dims, baseDims)
				}
			}
			if _, err := tx.db.Exec(ctx,
				`UPDATE knowledge_bases SET embedding_dims = $2, updated_at = now() WHERE id = $1`, kbID, dims); err != nil {
				return dbErr("replace chunks: set dims", err)
			}
		}
		if len(chunks) == 0 {
			return nil
		}
		batch := &pgx.Batch{}
		for i := range chunks {
			c := &chunks[i]
			c.DocumentID, c.KnowledgeBaseID = docID, kbID
			var emb *pgvector.Vector
			if c.Embedding != nil {
				v := pgvector.NewVector(c.Embedding)
				emb = &v
			}
			batch.Queue(
				`INSERT INTO knowledge_chunks (id, document_id, knowledge_base_id, seq, heading, content, embedding)
				 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7::vector)
				 RETURNING id`,
				nilIfZero(c.ID), docID, kbID, c.Seq, c.Heading, c.Content, emb,
			).QueryRow(func(row pgx.Row) error { return row.Scan(&c.ID) })
		}
		if err := tx.db.SendBatch(ctx, batch).Close(); err != nil {
			return dbErr("replace chunks: insert", err)
		}
		return nil
	})
}

// chunkDims returns the shared embedding dimension of chunks (0 when none is
// embedded) or ErrInvalid for mixed / inconsistent embeddings.
func chunkDims(chunks []domain.KnowledgeChunk) (int, error) {
	dims, embedded := 0, 0
	for i, c := range chunks {
		if c.Embedding == nil {
			continue
		}
		embedded++
		switch {
		case len(c.Embedding) == 0:
			return 0, fmt.Errorf("%w: chunk %d has an empty embedding", domain.ErrInvalid, i)
		case dims == 0:
			dims = len(c.Embedding)
		case len(c.Embedding) != dims:
			return 0, fmt.Errorf("%w: chunk %d has %d dimensions, expected %d", domain.ErrInvalid, i,
				len(c.Embedding), dims)
		}
	}
	if embedded != 0 && embedded != len(chunks) {
		return 0, fmt.Errorf("%w: either every chunk or none must carry an embedding", domain.ErrInvalid)
	}
	return dims, nil
}

// ListChunks pages a document's chunks in seq order (embeddings omitted) and
// returns the document's total chunk count.
func (s *Store) ListChunks(ctx context.Context, docID uuid.UUID, limit, offset int) ([]domain.KnowledgeChunk, int, error) {
	limit, offset = clampPage(limit, offset, 50, 500)
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM knowledge_chunks WHERE document_id = $1`, docID).
		Scan(&total); err != nil {
		return nil, 0, dbErr("list chunks", err)
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, document_id, knowledge_base_id, seq, heading, content FROM knowledge_chunks
		 WHERE document_id = $1 ORDER BY seq LIMIT $2 OFFSET $3`, docID, limit, offset)
	if err != nil {
		return nil, 0, dbErr("list chunks", err)
	}
	out, err := collect("list chunks", rows, func(row pgx.Row) (*domain.KnowledgeChunk, error) {
		var c domain.KnowledgeChunk
		if err := row.Scan(&c.ID, &c.DocumentID, &c.KnowledgeBaseID, &c.Seq, &c.Heading, &c.Content); err != nil {
			return nil, err
		}
		return &c, nil
	})
	return out, total, err
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// SearchVector returns the k chunks of the base nearest to embedding by
// cosine distance, with Score = 1 - distance. Chunks of another dimension
// are ignored.
func (s *Store) SearchVector(ctx context.Context, kbID uuid.UUID, embedding []float32, k int) ([]domain.KnowledgeHit, error) {
	if len(embedding) == 0 {
		return nil, fmt.Errorf("crm: search vector: %w: empty embedding", domain.ErrInvalid)
	}
	if k <= 0 {
		k = 5
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+knowledgeHitCols+`, 1 - (c.embedding <=> $2::vector) AS score
		   FROM knowledge_chunks c JOIN knowledge_documents d ON d.id = c.document_id
		  WHERE c.knowledge_base_id = $1 AND c.embedding IS NOT NULL AND vector_dims(c.embedding) = $3
		  ORDER BY c.embedding <=> $2::vector, c.id
		  LIMIT $4`,
		kbID, pgvector.NewVector(embedding), len(embedding), k)
	if err != nil {
		return nil, dbErr("search vector", err)
	}
	return collect("search vector", rows, scanKnowledgeHit)
}

// SearchText returns the k best full-text matches of query within the base.
// Passes, each only filling the slots the previous ones left:
//  1. websearch_to_tsquery('simple') (all words, quotes, -exclusions), ranked
//     by ts_rank_cd;
//  2. any word as a prefix (a crude stem, so inflected Mongolian/Cyrillic
//     forms such as "захиалга"/"захиалгын" still meet), ranked by ts_rank_cd;
//  3. for short queries, a case-insensitive substring match.
//
// Scores are ts_rank_cd values (pass 2 halved, pass 3 a small constant), so
// earlier passes always rank first.
func (s *Store) SearchText(ctx context.Context, kbID uuid.UUID, query string, k int) ([]domain.KnowledgeHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []domain.KnowledgeHit{}, nil
	}
	if k <= 0 {
		k = 5
	}
	out := make([]domain.KnowledgeHit, 0, k)
	seen := map[uuid.UUID]bool{}
	add := func(op, sql string, args ...any) error {
		if len(out) >= k {
			return nil
		}
		rows, err := s.db.Query(ctx, sql, args...)
		if err != nil {
			return dbErr(op, err)
		}
		hits, err := collect(op, rows, scanKnowledgeHit)
		if err != nil {
			return err
		}
		for _, h := range hits {
			if len(out) < k && !seen[h.ChunkID] {
				seen[h.ChunkID] = true
				out = append(out, h)
			}
		}
		return nil
	}

	if err := add("search text", `
		SELECT `+knowledgeHitCols+`, ts_rank_cd(c.tsv, q.tsq) AS score
		  FROM knowledge_chunks c JOIN knowledge_documents d ON d.id = c.document_id,
		       websearch_to_tsquery('simple', $2) AS q(tsq)
		 WHERE c.knowledge_base_id = $1 AND c.tsv @@ q.tsq
		 ORDER BY score DESC, c.id
		 LIMIT $3`, kbID, query, k); err != nil {
		return nil, err
	}
	if terms := prefixTerms(query); len(terms) > 0 {
		if err := add("search text (prefix)", `
			SELECT `+knowledgeHitCols+`, ts_rank_cd(c.tsv, q.tsq) / 2 AS score
			  FROM knowledge_chunks c JOIN knowledge_documents d ON d.id = c.document_id,
			       to_tsquery('simple', $2) AS q(tsq)
			 WHERE c.knowledge_base_id = $1 AND c.tsv @@ q.tsq
			 ORDER BY score DESC, c.id
			 LIMIT $3`, kbID, strings.Join(terms, " | "), k+len(out)); err != nil {
			return nil, err
		}
	}
	if utf8.RuneCountInString(query) <= shortQueryRunes {
		if err := add("search text (substring)", `
			SELECT `+knowledgeHitCols+`, 0.001::float8 AS score
			  FROM knowledge_chunks c JOIN knowledge_documents d ON d.id = c.document_id
			 WHERE c.knowledge_base_id = $1 AND (c.content ILIKE $2 OR c.heading ILIKE $2)
			 ORDER BY d.created_at, c.document_id, c.seq
			 LIMIT $3`, kbID, likePattern(query), k+len(out)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// shortQueryRunes is the query length up to which SearchText also tries a
// substring match.
const shortQueryRunes = 24

// prefixTerms turns a free-text query into to_tsquery prefix terms: each
// distinct word (letters/digits only, lower-cased) cut to a crude stem and
// suffixed with ":*". Words of one rune are dropped.
func prefixTerms(query string) []string {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		r := []rune(w)
		if len(r) < 2 {
			continue
		}
		if len(r) > 4 {
			keep := len(r) - 3
			if keep < 4 {
				keep = 4
			}
			r = r[:keep]
		}
		stem := string(r)
		if !seen[stem] {
			seen[stem] = true
			out = append(out, stem+":*")
		}
	}
	return out
}

// AllChunksText concatenates the base's chunks as "## <heading>\n<content>"
// blocks in document (created_at) and seq order, stopping at maxChars runes
// (truncated=true when something was left out). A heading is repeated only
// when it changes, and the overlap a chunk shares with its predecessor in the
// same document is dropped so the context carries each passage once.
func (s *Store) AllChunksText(ctx context.Context, kbID uuid.UUID, maxChars int) (string, bool, error) {
	if maxChars <= 0 {
		return "", false, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT c.document_id, c.heading, c.content
		   FROM knowledge_chunks c JOIN knowledge_documents d ON d.id = c.document_id
		  WHERE c.knowledge_base_id = $1
		  ORDER BY d.created_at, d.id, c.seq`, kbID)
	if err != nil {
		return "", false, dbErr("all chunks text", err)
	}
	defer rows.Close()

	var (
		b         strings.Builder
		used      int
		prevDoc   uuid.UUID
		prevHead  string
		prevBody  string
		truncated bool
	)
	for rows.Next() {
		var docID uuid.UUID
		var heading, content string
		if err := rows.Scan(&docID, &heading, &content); err != nil {
			return "", false, dbErr("all chunks text", err)
		}
		body := content
		sameDoc := docID == prevDoc
		if sameDoc {
			body = strings.TrimSpace(body[overlapLen(prevBody, content):])
		}
		var piece strings.Builder
		if used > 0 {
			piece.WriteString("\n\n")
		}
		if heading != "" && (!sameDoc || heading != prevHead) {
			piece.WriteString("## ")
			piece.WriteString(heading)
			piece.WriteString("\n")
		}
		piece.WriteString(body)
		prevDoc, prevHead, prevBody = docID, heading, content
		if body == "" {
			continue
		}
		p := piece.String()
		n := utf8.RuneCountInString(p)
		if used+n > maxChars {
			b.WriteString(cutRunes(p, maxChars-used))
			truncated = true
			break
		}
		b.WriteString(p)
		used += n
	}
	if err := rows.Err(); err != nil {
		return "", false, dbErr("all chunks text", err)
	}
	return strings.TrimSpace(b.String()), truncated, nil
}

// overlapLen returns the byte length of the longest prefix of cur (ending at
// a word boundary, at least 16 bytes) that is also a suffix of prev.
func overlapLen(prev, cur string) int {
	const maxOverlap, minOverlap = 4096, 16
	limit := len(cur)
	if limit > maxOverlap {
		limit = maxOverlap
	}
	if limit > len(prev) {
		limit = len(prev)
	}
	for l := limit; l >= minOverlap; l-- {
		if l < len(cur) && cur[l] != ' ' && cur[l] != '\n' {
			continue // only cut at a word boundary
		}
		if strings.HasSuffix(prev, cur[:l]) {
			return l
		}
	}
	return 0
}

// cutRunes returns at most n runes of s, preferring to end at whitespace.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			cut := s[:pos]
			if sp := strings.LastIndexAny(cut, " \n"); sp > len(cut)/2 {
				cut = cut[:sp]
			}
			return strings.TrimSpace(cut)
		}
		i++
	}
	return s
}
