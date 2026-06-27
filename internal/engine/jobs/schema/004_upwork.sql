-- 004_upwork.sql: Add Upwork profile fields to resume_persons.
-- Reset search_path to avoid ag_catalog contamination from 002_resume_graph.sql.
SET search_path TO public;

-- headline: the Upwork profile title set by the operator (shown as "Title" on Upwork, max 70 chars).
ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS headline TEXT;

-- hourly_rate: Upwork hourly rate stored as integer cents (e.g. 12000 = $120.00/hr)
-- matching the hunt_bounties.amount_cents convention.
ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS hourly_rate BIGINT; -- unit: cents (100 = $1.00/hr)
