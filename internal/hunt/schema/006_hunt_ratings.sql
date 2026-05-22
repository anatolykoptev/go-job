CREATE TABLE IF NOT EXISTS hunt_ratings (
    id         BIGSERIAL PRIMARY KEY,
    entry_kind TEXT NOT NULL,
    entry_id   BIGINT NOT NULL,
    user_name  TEXT NOT NULL DEFAULT 'krolik',
    stage      TEXT NOT NULL DEFAULT 'new',
    note       TEXT,
    rated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (entry_kind, entry_id, user_name)
);
CREATE INDEX IF NOT EXISTS idx_hunt_ratings_lookup ON hunt_ratings (entry_kind, entry_id);
CREATE INDEX IF NOT EXISTS idx_hunt_ratings_stage ON hunt_ratings (stage, updated_at DESC);
