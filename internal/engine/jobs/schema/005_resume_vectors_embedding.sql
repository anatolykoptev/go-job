-- soft
SET search_path TO public;

-- No-op on our box (pgvector 0.8.2 already installed).
-- On a public postgres without the extension, this migration fails gracefully
-- (warn-and-continue pattern, same as 002_resume_graph.sql for Apache AGE).
-- When absent, the embedding column is skipped and FTS is used instead.
CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE resume_vectors ADD COLUMN IF NOT EXISTS embedding vector(1024);

-- No ANN index at ≤500 rows: exact scan is faster to maintain and more accurate.
-- When the table grows past ~10k rows, add:
--   CREATE INDEX ON resume_vectors USING hnsw (embedding vector_cosine_ops);
