-- 009_hunt_jobs_recommendation.sql
-- go-job is the sole DDL owner of hunt_jobs (ADR-go-job-001). This migration
-- makes go-job's own schema complete: it adds the fit_score index and the
-- curator recommendation fields that go-nerv's 007_hunt_jobs_fit_score.sql +
-- 008_hunt_jobs_recommendation.sql previously owned on the shared DB.
-- All statements are idempotent (IF NOT EXISTS). The fit_score COLUMN itself is
-- already added by 008_hunt_job_scores.sql; here we add only its index plus the
-- recommendation_* fields and their partial index.

-- fit_score index (column already present from 008_hunt_job_scores.sql).
CREATE INDEX IF NOT EXISTS hunt_jobs_fit_score_idx
    ON hunt_jobs (fit_score DESC NULLS LAST);

-- Curator recommendation fields. Nullable — only manually-curated roles carry a tier.
ALTER TABLE hunt_jobs
    ADD COLUMN IF NOT EXISTS recommendation_tier text
        CHECK (recommendation_tier IN ('A','B','C') OR recommendation_tier IS NULL),
    ADD COLUMN IF NOT EXISTS recommendation_rank integer,
    ADD COLUMN IF NOT EXISTS recommendation_note text;

-- Partial index: only rows actually recommended (< 50 at any time).
CREATE INDEX IF NOT EXISTS hunt_jobs_recommendation_rank_idx
    ON hunt_jobs (recommendation_rank ASC NULLS LAST)
    WHERE recommendation_tier IS NOT NULL;
