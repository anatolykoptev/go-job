-- soft
-- 009_resume_master_variant_backfill.sql: mark the latest person as master.
--
-- One-shot data backfill (DO $$ block) marked -- soft to pass the idempotency
-- guard (TestResumeDB_Migrate_PgUtil_FreshDB) which cannot prove logical
-- idempotency of an UPDATE statically. The backfill IS logically idempotent:
-- after the first run a master row exists and the NOT EXISTS predicate is
-- false, so re-run is a no-op. Soft semantics (warn + skip + retry on failure,
-- not abort) are safe here — a retry re-runs a no-op UPDATE. Same pattern as
-- 007_resume_vectors_source_backfill.sql.
--
-- Split from 008_resume_master_variant.sql: the DDL (columns + indexes) is
-- load-bearing and non-soft in 008; this backfill is a one-shot data migration
-- and stays soft. The columns it depends on (is_master) are added by 008,
-- which runs first in lexical order.
SET search_path TO public;

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
