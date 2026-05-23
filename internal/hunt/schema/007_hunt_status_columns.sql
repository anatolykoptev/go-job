-- Migration 007: status enrichment columns on hunt_bounties, hunt_jobs, hunt_freelance, hunt_security.
-- hunt_audit_contests is excluded — ends_at < NOW() is computed status.
-- Idempotent: all DDL uses IF NOT EXISTS / safe defaults.

ALTER TABLE hunt_bounties ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'open';
ALTER TABLE hunt_bounties ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ;
ALTER TABLE hunt_bounties ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_hunt_bounties_status ON hunt_bounties (status) WHERE status != 'open';
CREATE INDEX IF NOT EXISTS idx_hunt_bounties_check_due ON hunt_bounties (last_checked_at NULLS FIRST) WHERE status = 'open';

ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'open';
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ;
ALTER TABLE hunt_jobs ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_status ON hunt_jobs (status) WHERE status != 'open';
CREATE INDEX IF NOT EXISTS idx_hunt_jobs_check_due ON hunt_jobs (last_checked_at NULLS FIRST) WHERE status = 'open';

ALTER TABLE hunt_freelance ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'open';
ALTER TABLE hunt_freelance ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ;
ALTER TABLE hunt_freelance ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;

ALTER TABLE hunt_security ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'open';
ALTER TABLE hunt_security ADD COLUMN IF NOT EXISTS closed_at TIMESTAMPTZ;
ALTER TABLE hunt_security ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;
