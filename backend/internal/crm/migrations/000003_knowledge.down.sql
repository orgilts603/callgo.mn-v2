DROP TABLE IF EXISTS knowledge_chunks;
DROP TABLE IF EXISTS knowledge_documents;
ALTER TABLE agent_profiles
    DROP CONSTRAINT IF EXISTS agent_profiles_kb_fk,
    DROP COLUMN IF EXISTS knowledge_base_id,
    DROP COLUMN IF EXISTS knowledge_mode;
DROP TABLE IF EXISTS knowledge_bases;
