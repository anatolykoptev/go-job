-- Migration 012: split hunt_ratings.stage into two orthogonal axes.
-- ADR-go-job-003 addendum (2026-06-29): reverses the one-funnel call.
--
-- Before: stage ∈ {new,interesting,saved,discarded,claimed,applied,interview,offer,rejected}
-- After:
--   triage ∈ {interesting,saved,discarded,''} — operator interest signal ('' = untriaged)
--   stage  ∈ {claimed,applied,interview,offer,rejected,''} — pipeline position ('' = not in pipeline)
--
-- Idempotent: all statements use IF NOT EXISTS / CASE guards so re-running on an
-- already-migrated DB is a no-op.

-- Step 1: add triage column (empty default = untriaged).
ALTER TABLE hunt_ratings ADD COLUMN IF NOT EXISTS triage TEXT NOT NULL DEFAULT '';

-- Step 2: change stage default from 'new' to '' (pipeline start = not in pipeline).
-- Safe: only affects future INSERTs; existing rows backfilled below.
ALTER TABLE hunt_ratings ALTER COLUMN stage SET DEFAULT '';

-- Step 3: idempotent backfill.
-- Guard: only touch rows whose stage is still a pre-split triage/legacy value
-- (i.e. triage is still '' AND stage is a value that belongs on the triage axis now).
-- Rows already backfilled (triage != '' OR stage is a pipeline/empty value) are untouched.

-- 3a. interesting/saved/discarded → triage=stage, stage=''
UPDATE hunt_ratings
   SET triage = stage,
       stage  = ''
 WHERE stage IN ('interesting', 'saved', 'discarded')
   AND triage = '';

-- 3b. 'new' → both axes empty (untriaged + not in pipeline)
UPDATE hunt_ratings
   SET stage = ''
 WHERE stage = 'new'
   AND triage = '';

-- 3c. pipeline values (claimed/applied/interview/offer/rejected) → stay in stage, triage stays ''
-- No-op: these rows already have triage='' (default) and correct stage value.
-- Listed here for documentation completeness only.

-- Step 4: index on triage for shortlist/filter queries.
CREATE INDEX IF NOT EXISTS idx_hunt_ratings_triage ON hunt_ratings (triage, updated_at DESC);
