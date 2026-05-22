CREATE TABLE IF NOT EXISTS oversize_responses (
    id BIGSERIAL PRIMARY KEY,
    tool_name TEXT NOT NULL,
    query_hash TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL,
    size_bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    sample JSONB,
    item_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_oversize_responses_tool_created
    ON oversize_responses (tool_name, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_oversize_responses_sha256
    ON oversize_responses (sha256);
