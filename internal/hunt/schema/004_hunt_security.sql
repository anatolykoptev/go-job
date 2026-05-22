CREATE TABLE IF NOT EXISTS hunt_security (
    id            BIGSERIAL PRIMARY KEY,
    dedup_hash    TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    url           TEXT NOT NULL,
    platform      TEXT NOT NULL,
    program_type  TEXT,
    min_bounty    INT,
    max_bounty    INT,
    targets       TEXT[],
    managed       BOOLEAN NOT NULL DEFAULT FALSE,
    description   TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw           JSONB
);
CREATE INDEX IF NOT EXISTS idx_hunt_security_platform ON hunt_security (platform);
CREATE INDEX IF NOT EXISTS idx_hunt_security_max_bounty ON hunt_security (max_bounty DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_security_last_seen ON hunt_security (last_seen_at DESC);
