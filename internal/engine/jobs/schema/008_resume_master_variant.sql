-- soft
-- 008_resume_master_variant.sql: Master/variant resume model + account_id FK.
--
-- Phase 11 (Multi-User) schema foundation. Adds three columns to resume_persons:
--
--   is_master   BOOLEAN  — exactly one person per account is the master resume.
--                          Variants (tailored versions) carry is_master=false and
--                          parent_id pointing at their master.
--   parent_id   INT      — FK to resume_persons(id); NULL for a master, non-NULL
--                          for a variant derived from that master.
--   account_id  UUID     — FK to panel_accounts(id); NULL in the expand phase
--                          (backfilled to the first operator's account in the
--                          constrain phase — the one-way door per roadmap Phase 11).
--
-- This is the EXPAND + BACKFILL phase. The CONSTRAIN phase (NOT NULL flip on
-- account_id + partial unique index on (account_id) WHERE is_master) is a
-- separate migration after the restore has been rehearsed. That flip is the
-- one-way door; everything here is reversible.
--
-- Marked -- soft because the DO $$ block (backfill UPDATE) is a one-shot data
-- migration that the idempotency guard (TestResumeMigrationsIdempotent) cannot
-- prove statically. The UPDATE IS logically idempotent: after the first run a
-- master row exists and the NOT EXISTS predicate is false, so re-run is a no-op.
-- Soft semantics (warn + skip + retry on failure, not abort) are safe here —
-- a retry re-runs a no-op UPDATE. Same pattern as 007_resume_vectors_source_backfill.sql.
SET search_path TO public;

-- Expand: add columns.
ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS is_master BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS parent_id INT REFERENCES resume_persons(id) ON DELETE SET NULL;
ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS account_id UUID;

-- Backfill: mark the latest person as master (single-user assumption — the
-- existing operator has one person row, the most recently created one).
-- Guarded by NOT EXISTS so it is idempotent: once any master exists, the
-- backfill is a no-op on re-run.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM resume_persons WHERE is_master = true) THEN
    UPDATE resume_persons
       SET is_master = true
     WHERE id = (SELECT max(id) FROM resume_persons);
  END IF;
END $$;

-- Index for the master-lookup hot path: GetMasterPersonID.
-- Partial index — only master rows, one per account (enforced in the constrain
-- phase by a unique partial index; for now a plain index suffices for lookups).
CREATE INDEX IF NOT EXISTS idx_resume_persons_master
    ON resume_persons(is_master) WHERE is_master = true;

-- Index for variant lookups by parent (used by the refinement pipeline to
-- find all variants of a master).
CREATE INDEX IF NOT EXISTS idx_resume_persons_parent
    ON resume_persons(parent_id) WHERE parent_id IS NOT NULL;

-- Index for account-scoped queries (Phase 11 constrain phase will make
-- account_id NOT NULL; this index supports the eventual per-account scan).
CREATE INDEX IF NOT EXISTS idx_resume_persons_account
    ON resume_persons(account_id) WHERE account_id IS NOT NULL;
