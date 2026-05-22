CREATE TABLE IF NOT EXISTS hunt_bounties (
    id            BIGSERIAL PRIMARY KEY,
    dedup_hash    TEXT NOT NULL UNIQUE,
    title         TEXT NOT NULL,
    url           TEXT NOT NULL,
    org           TEXT,
    source        TEXT NOT NULL,
    amount_cents  BIGINT,
    currency      TEXT,
    issue_number  INT,
    skills        TEXT[],
    description   TEXT,
    relevance     REAL,
    posted_at     TIMESTAMPTZ,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw           JSONB
);
CREATE INDEX IF NOT EXISTS idx_hunt_bounties_source_posted ON hunt_bounties (source, posted_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_bounties_amount ON hunt_bounties (amount_cents DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_bounties_last_seen ON hunt_bounties (last_seen_at DESC);
CREATE INDEX IF NOT EXISTS idx_hunt_bounties_skills ON hunt_bounties USING GIN (skills);
