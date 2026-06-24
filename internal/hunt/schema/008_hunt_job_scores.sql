ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS fit_score        INT;
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS fit_band         TEXT;
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS success_band     TEXT;
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS over_under       TEXT;
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS score_rationale  JSONB;
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS scored_at        TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_unscored ON hunt_jobs (first_seen_at) WHERE scored_at IS NULL AND status = 'open';
