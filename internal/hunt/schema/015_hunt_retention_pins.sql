-- 015_hunt_retention_pins.sql — rows the retention prune must never delete.
--
-- Retention (hunt.PruneStale) deletes corpus rows not seen for
-- HUNT_RETENTION_DAYS unless the row is rated (hunt_ratings) or pinned here.
-- A pin is not a user judgement and is never read by the tracker or shortlist:
--   artifact       application_persist wrote files keyed by this job id
--                  (or the artifact backfill found them on disk)
--   legacy_company the operator has a legacy application directory for this
--                  job's company (directory names carry no job id)
--
-- Polymorphic like hunt_ratings (entry_kind, entry_id), so no FK to the
-- per-kind tables. A pin whose row is gone is inert.
CREATE TABLE IF NOT EXISTS hunt_retention_pins (
    entry_kind TEXT        NOT NULL CHECK (entry_kind IN ('job', 'freelance', 'security')),
    entry_id   BIGINT      NOT NULL,
    reason     TEXT        NOT NULL CHECK (reason IN ('artifact', 'legacy_company')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (entry_kind, entry_id)
);
