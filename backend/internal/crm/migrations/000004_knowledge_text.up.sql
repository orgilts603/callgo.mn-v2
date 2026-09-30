-- Keep the extracted plain text of each knowledge document so it can be
-- re-chunked / re-embedded (reprocess, chunk-size change, new embedder)
-- without the original upload, which is never stored.
ALTER TABLE knowledge_documents ADD COLUMN extracted_text text;
