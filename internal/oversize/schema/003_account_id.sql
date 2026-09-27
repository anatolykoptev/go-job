-- 003_account_id.sql — per-account oversize spill scope (plan ADR-10, P4).
-- Nullable + FK guarded on panel_accounts (oversize migrates before
-- accounts.Bootstrap on fresh DBs; accounts.EnsureOversizeAccountScope retries
-- the FK). Backfill to the operator account lives in Bootstrap.
ALTER TABLE oversize_responses ADD COLUMN IF NOT EXISTS account_id UUID;

DO $$
BEGIN
    IF to_regclass('public.panel_accounts') IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
           WHERE conname = 'oversize_responses_account_id_fkey'
             AND conrelid = 'oversize_responses'::regclass
       ) THEN
        ALTER TABLE oversize_responses
            ADD CONSTRAINT oversize_responses_account_id_fkey
            FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_oversize_responses_account
    ON oversize_responses (account_id, created_at DESC);
