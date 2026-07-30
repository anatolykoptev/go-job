-- soft
-- 007_resume_vectors_source_backfill.sql: backfill the source discriminator on
-- pre-existing derived rows.
--
-- Marked -- soft not because it depends on an optional extension, but because it
-- is a one-shot data backfill (an UPDATE, not idempotent DDL). The resumedb
-- runner uses pgutil with NO Baseline, so on the cutover boot every non-soft
-- file is re-executed; the idempotency guard (TestResumeMigrationsIdempotent)
-- flags any non-soft UPDATE because it cannot prove logical idempotency
-- statically. This UPDATE IS logically idempotent: after the first run the
-- matched rows carry source='profile', so the `source = 'agent'` predicate no
-- longer matches on re-run. Soft semantics (warn + skip + retry on failure, not
-- abort) are safe here — a retry re-runs a no-op UPDATE.
--
-- schema/004 introduced `source TEXT NOT NULL DEFAULT 'agent'` but no migration
-- backfilled it. Every derived row written by master_resume / resume_enrich
-- BEFORE this branch carries source='agent' (the column default), so
-- ListDerivedVectors (filtering source='profile') returns nothing for them and
-- SyncProfileVectors adds fresh duplicates alongside the stale ones.
--
-- Safe predicate: manual free-text memories always have ref_id IS NULL
-- (AddResumeMemory → UpsertVector with refID=nil; UpdateResumeMemory preserves
-- the existing ref_id). Derived rows always carry the entity id as ref_id
-- (master_resume / SyncProfileVectors pass &entityID, non-nil). Relabel only
-- the ref_id-bearing rows to source='profile'.
--
-- ref_id IS NULL rows (manual memories) are never touched under any circumstance.
SET search_path TO public;

UPDATE resume_vectors
   SET source = 'profile'
 WHERE ref_id IS NOT NULL
   AND source = 'agent';
