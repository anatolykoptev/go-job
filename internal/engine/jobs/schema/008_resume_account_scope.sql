-- 008_resume_account_scope.sql — per-account resume scope (plan ADR-6/ADR-9, P4).
--
-- resume_persons becomes the account-owned hub: every resume child table
-- (experiences/educations/skills/projects/achievements/certifications/domains/
-- methodologies/upwork_*) stays scoped transitively via person_id ON DELETE
-- CASCADE. resume_vectors moves its scoping key from user_name ('gojob') to
-- account_id — the column itself stays until the post-soak drop (ADR-13).
--
-- Ordering notes (load-bearing):
--   - ConnectResumeDB (this runner) executes BEFORE accounts.Bootstrap, so on a
--     fresh DB panel_accounts does not exist yet when this file runs. Both FKs
--     are therefore added via guarded DO blocks that probe to_regclass — an
--     inline REFERENCES would fail the standalone/fresh-DB apply. When
--     panel_accounts is absent the column lands unscoped and
--     accounts.EnsureResumeAccountScope (Bootstrap) adds the FK later — same
--     shape as hunt schema 014.
--   - The UNIQUE swap tolerates pre-backfill NULLs (NULLs are distinct), and
--     NULL-enforcement is deferred to P5 (ADR-13 expand/backfill/constrain).
--   - accounts.Bootstrap.BackfillResumeAccountData stamps existing rows to the
--     operator account; content_hash is re-keyed to the account there.
SET search_path TO public;

ALTER TABLE resume_persons ADD COLUMN IF NOT EXISTS account_id UUID;
ALTER TABLE resume_vectors ADD COLUMN IF NOT EXISTS account_id UUID;

DO $$
BEGIN
    IF to_regclass('public.panel_accounts') IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
           WHERE conname = 'resume_persons_account_id_fkey'
             AND conrelid = 'resume_persons'::regclass
       ) THEN
        ALTER TABLE resume_persons
            ADD CONSTRAINT resume_persons_account_id_fkey
            FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.panel_accounts') IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
           WHERE conname = 'resume_vectors_account_id_fkey'
             AND conrelid = 'resume_vectors'::regclass
       ) THEN
        ALTER TABLE resume_vectors
            ADD CONSTRAINT resume_vectors_account_id_fkey
            FOREIGN KEY (account_id) REFERENCES panel_accounts(id);
    END IF;
END $$;

-- Swap the vector dedup scope: user_name stops being the scoping key (the
-- column itself stays until the post-soak drop, ADR-13).
ALTER TABLE resume_vectors DROP CONSTRAINT IF EXISTS resume_vectors_user_name_content_hash_key;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'resume_vectors_account_content_key'
          AND conrelid = 'resume_vectors'::regclass
    ) THEN
        ALTER TABLE resume_vectors
            ADD CONSTRAINT resume_vectors_account_content_key
            UNIQUE (account_id, content_hash);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_resume_persons_account ON resume_persons (account_id);
CREATE INDEX IF NOT EXISTS idx_resume_vectors_account ON resume_vectors (account_id);
