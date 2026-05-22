CREATE TABLE IF NOT EXISTS hunt_audit_contests (
    id            BIGSERIAL PRIMARY KEY,
    dedup_hash    TEXT NOT NULL UNIQUE,
    title         TEXT NOT NULL,
    url           TEXT NOT NULL,
    platform      TEXT NOT NULL,
    total_pool    INT,
    currency      TEXT,
    starts_at     TIMESTAMPTZ,
    ends_at       TIMESTAMPTZ,
    languages     TEXT[],
    description   TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw           JSONB
);
CREATE INDEX IF NOT EXISTS idx_hunt_audit_contests_platform_starts ON hunt_audit_contests (platform, starts_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_audit_contests_pool ON hunt_audit_contests (total_pool DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_audit_contests_last_seen ON hunt_audit_contests (last_seen_at DESC);
