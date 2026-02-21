-- 003_resume_extended.sql: Extend resume schema with domains, methodologies, and metadata.

-- Reset search_path to avoid ag_catalog contamination from 002_resume_graph.sql.
SET search_path TO public;

-- Extend experiences with metadata
ALTER TABLE resume_experiences ADD COLUMN IF NOT EXISTS team_size INT;
ALTER TABLE resume_experiences ADD COLUMN IF NOT EXISTS budget_usd INT;
ALTER TABLE resume_experiences ADD COLUMN IF NOT EXISTS domain TEXT;
ALTER TABLE resume_experiences ADD COLUMN IF NOT EXISTS is_volunteer BOOLEAN DEFAULT FALSE;

-- Extend skills with provenance
ALTER TABLE resume_skills ADD COLUMN IF NOT EXISTS is_implicit BOOLEAN DEFAULT FALSE;
ALTER TABLE resume_skills ADD COLUMN IF NOT EXISTS source TEXT DEFAULT 'resume';  -- 'resume', 'inferred', 'enrichment'

-- Extend achievements with parsed metrics
ALTER TABLE resume_achievements ADD COLUMN IF NOT EXISTS metric_numeric FLOAT;
ALTER TABLE resume_achievements ADD COLUMN IF NOT EXISTS metric_unit TEXT;

-- Extend projects with parent link
ALTER TABLE resume_projects ADD COLUMN IF NOT EXISTS parent_experience_id INT REFERENCES resume_experiences(id) ON DELETE SET NULL;

-- New: domains table (schema-qualified to prevent ag_catalog contamination)
CREATE TABLE IF NOT EXISTS public.resume_domains (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES public.resume_persons(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    UNIQUE(person_id, name)
);

-- New: methodologies table (schema-qualified to prevent ag_catalog contamination)
CREATE TABLE IF NOT EXISTS public.resume_methodologies (
    id          SERIAL PRIMARY KEY,
    person_id   INT REFERENCES public.resume_persons(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT,
    UNIQUE(person_id, name)
);

-- Enrichment tracking
ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS enriched_at TIMESTAMPTZ;
