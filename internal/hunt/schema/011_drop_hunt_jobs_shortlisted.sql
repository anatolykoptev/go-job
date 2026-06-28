-- 011_drop_hunt_jobs_shortlisted.sql
-- Removes the hunt_jobs.shortlisted boolean column added in 010.
-- The star feature is now backed by hunt_ratings (stage-based membership)
-- so the parallel boolean axis is dead weight.
-- Both statements are idempotent (IF EXISTS).

DROP INDEX IF EXISTS hunt_jobs_shortlisted_idx;
ALTER TABLE hunt_jobs DROP COLUMN IF EXISTS shortlisted;
