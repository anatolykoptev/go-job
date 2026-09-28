-- 014_hunt_ratings_account.sql — per-account ratings (plan ADR-6/ADR-15).
--
-- hunt_ratings rows are per-account JUDGMENTS (triage + pipeline stage + note),
-- not shared corpus: two accounts rating the same hunt_jobs row must hold
-- independent rows. The scoping key moves user_name → account_id (FK to
-- panel_accounts); the old UNIQUE(entry_kind, entry_id, user_name) becomes
-- UNIQUE(entry_kind, entry_id, account_id).
--
-- Ordering notes (load-bearing):
--   - Production boots accounts.Bootstrap BEFORE hStore.Migrate (ADR-6), so
--     panel_accounts provably exists when this file runs there. The CI schema
--     sweep applies */schema/*.sql standalone to a bare DB — panel_accounts is
--     absent — so the FK is added via a guarded DO block instead of inline
--     REFERENCES (an inline REFERENCES would fail the standalone apply).
--   - accounts.Bootstrap.BackfillHuntRatingsAccount already added the column
--     and backfilled existing rows to the operator account on a live DB
--     (Bootstrap precedes Migrate), so every statement here is written
--     idempotent: ADD COLUMN IF NOT EXISTS / DO-block constraint guards /
--     CREATE INDEX IF NOT EXISTS.
--   - The new UNIQUE tolerates pre-backfill NULLs (NULLs are distinct), and
--     NULL-enforcement is deferred to P5 (ADR-13 expand/backfill/constrain).
ALTER TABLE hunt_ratings ADD COLUMN IF NOT EXISTS account_id UUID;

DO $$
BEGIN
    IF to_regclass('public.panel_accounts') IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
           WHERE conname = 'hunt_ratings_account_id_fkey'
             AND conrelid = 'hunt_ratings'::regclass
       ) THEN
        ALTER TABLE hunt_ratings
            ADD CONSTRAINT hunt_ratings_account_id_fkey
            FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
    END IF;
END $$;

-- Swap the unique scope: user_name stops being the scoping key (the column
-- itself stays until the post-soak drop, ADR-13).
ALTER TABLE hunt_ratings DROP CONSTRAINT IF EXISTS hunt_ratings_entry_kind_entry_id_user_name_key;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'hunt_ratings_entry_account_key'
          AND conrelid = 'hunt_ratings'::regclass
    ) THEN
        ALTER TABLE hunt_ratings
            ADD CONSTRAINT hunt_ratings_entry_account_key
            UNIQUE (entry_kind, entry_id, account_id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_hunt_ratings_account ON hunt_ratings (account_id);
