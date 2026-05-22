CREATE TABLE IF NOT EXISTS hunt_jobs (
    id              BIGSERIAL PRIMARY KEY,
    dedup_hash      TEXT NOT NULL UNIQUE,
    title           TEXT NOT NULL,
    company         TEXT,
    url             TEXT NOT NULL,
    source          TEXT NOT NULL,
    external_id     TEXT,
    location        TEXT,
    remote          TEXT,
    job_type        TEXT,
    experience      TEXT,
    salary_min      INT,
    salary_max      INT,
    salary_currency TEXT,
    salary_interval TEXT,
    skills          TEXT[],
    tags            TEXT[],
    description     TEXT,
    posted_at       TIMESTAMPTZ,
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw             JSONB
);
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_source_posted ON hunt_jobs (source, posted_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_salary ON hunt_jobs (salary_max DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_company ON hunt_jobs (company) WHERE company IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_skills ON hunt_jobs USING GIN (skills);
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_remote ON hunt_jobs (remote) WHERE remote IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_last_seen ON hunt_jobs (last_seen_at DESC);
