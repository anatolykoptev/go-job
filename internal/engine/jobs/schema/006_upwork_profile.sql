-- 006_upwork_profile.sql: Upwork-specific profile tables.
-- Keeps Upwork bounded context separate from the resume context (which stores
-- headline + hourly_rate on resume_persons for backward compat with edit UI).
-- Reset search_path to avoid ag_catalog contamination from 002_resume_graph.sql.
SET search_path TO public;

CREATE TABLE IF NOT EXISTS upwork_profile (
    person_id    INT PRIMARY KEY REFERENCES resume_persons(id) ON DELETE CASCADE,
    title        TEXT,
    overview     TEXT,
    hourly_rate  BIGINT,
    categories   TEXT[],
    availability TEXT,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS upwork_skills (
    id         SERIAL PRIMARY KEY,
    person_id  INT NOT NULL REFERENCES resume_persons(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    position   INT NOT NULL DEFAULT 0,
    UNIQUE(person_id, name)
);

CREATE TABLE IF NOT EXISTS upwork_catalog_items (
    id          SERIAL PRIMARY KEY,
    person_id   INT NOT NULL REFERENCES resume_persons(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT,
    position    INT NOT NULL DEFAULT 0
);
