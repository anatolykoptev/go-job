CREATE TABLE IF NOT EXISTS hunt_freelance (
    id              BIGSERIAL PRIMARY KEY,
    dedup_hash      TEXT NOT NULL UNIQUE,
    title           TEXT NOT NULL,
    url             TEXT NOT NULL,
    platform        TEXT NOT NULL,
    source          TEXT NOT NULL,
    budget_min      INT,
    budget_max      INT,
    budget_currency TEXT,
    budget_raw      TEXT,
    location        TEXT,
    skills          TEXT[],
    tags            TEXT[],
    description     TEXT,
    client_info     TEXT,
    posted_at       TIMESTAMPTZ,
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw             JSONB
);
CREATE INDEX IF NOT EXISTS idx_hunt_freelance_platform_posted ON hunt_freelance (platform, posted_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_freelance_budget ON hunt_freelance (budget_max DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_freelance_skills ON hunt_freelance USING GIN (skills);
CREATE INDEX IF NOT EXISTS idx_hunt_freelance_last_seen ON hunt_freelance (last_seen_at DESC);
