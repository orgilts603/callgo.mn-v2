-- Knowledge bases (RAG) backed by pgvector.
CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE agent_profiles
    ADD COLUMN knowledge_base_id uuid,
    ADD COLUMN knowledge_mode    text NOT NULL DEFAULT 'off'
                                 CHECK (knowledge_mode IN ('off', 'tool', 'context'));

CREATE TABLE knowledge_bases (
    id                      uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name                    text        NOT NULL,
    description             text        NOT NULL DEFAULT '',
    embedding_llm_config_id uuid        REFERENCES llm_configs (id) ON DELETE SET NULL,
    embedding_model         text        NOT NULL DEFAULT '',
    embedding_dims          integer     NOT NULL DEFAULT 0,
    chunk_size              integer     NOT NULL DEFAULT 1200 CHECK (chunk_size BETWEEN 200 AND 8000),
    chunk_overlap           integer     NOT NULL DEFAULT 200  CHECK (chunk_overlap >= 0),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX knowledge_bases_org_idx ON knowledge_bases (org_id, created_at DESC);

ALTER TABLE agent_profiles
    ADD CONSTRAINT agent_profiles_kb_fk FOREIGN KEY (knowledge_base_id)
        REFERENCES knowledge_bases (id) ON DELETE SET NULL;

CREATE TABLE knowledge_documents (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_base_id uuid        NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    org_id            uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    filename          text        NOT NULL,
    mime_type         text        NOT NULL DEFAULT '',
    size_bytes        bigint      NOT NULL DEFAULT 0,
    status            text        NOT NULL DEFAULT 'processing'
                                  CHECK (status IN ('processing', 'ready', 'failed')),
    error             text        NOT NULL DEFAULT '',
    chunk_count       integer     NOT NULL DEFAULT 0,
    char_count        integer     NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX knowledge_documents_kb_idx ON knowledge_documents (knowledge_base_id, created_at);

-- Embedding dimension is not fixed at the column level (providers differ:
-- 768 / 1536 / 3072); the base records its dims and every chunk in a base
-- shares them, so distance queries stay valid. An HNSW index needs a fixed
-- dimension, so we use an expression-free btree on kb and rely on sequential
-- scans per base (bases are small: thousands of chunks, not millions).
CREATE TABLE knowledge_chunks (
    id                uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id       uuid    NOT NULL REFERENCES knowledge_documents (id) ON DELETE CASCADE,
    knowledge_base_id uuid    NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    seq               integer NOT NULL,
    heading           text    NOT NULL DEFAULT '',
    content           text    NOT NULL,
    embedding         vector,
    tsv               tsvector GENERATED ALWAYS AS (to_tsvector('simple', heading || ' ' || content)) STORED,
    UNIQUE (document_id, seq)
);
CREATE INDEX knowledge_chunks_kb_idx  ON knowledge_chunks (knowledge_base_id);
CREATE INDEX knowledge_chunks_tsv_idx ON knowledge_chunks USING gin (tsv);
