-- 010_hunt_jobs_shortlisted.sql
-- Adds a simple boolean shortlisted flag to hunt_jobs so the admin UI can
-- star/un-star individual jobs without touching hunt_ratings.
-- All statements are idempotent (IF NOT EXISTS). Constant DEFAULT means Postgres
-- adds the column in-place with zero table rewrite on PG11+ (no downtime).

ALTER TABLE hunt_jobs
    ADD COLUMN IF NOT EXISTS shortlisted boolean NOT NULL DEFAULT false;

-- Partial index: only shortlisted=true rows (typically a small set) — keeps
-- the /admin/jobs?shortlisted=true filter fast at any table size.
CREATE INDEX IF NOT EXISTS hunt_jobs_shortlisted_idx
    ON hunt_jobs (shortlisted)
    WHERE shortlisted;
