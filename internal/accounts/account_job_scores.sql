-- account_job_scores.sql — per-account fit scores (plan ADR-6/ADR-15).
-- Accounts-owned DDL applied inside accounts.Bootstrap, right after
-- EnsureSchema creates the panel_accounts this table FK-references — same
-- rule as mcp_api_keys.sql: NEVER a standard migration root (CI psql-applies
-- */schema/*.sql standalone, before any EnsureSchema). Kept OUT of every
-- */schema/ directory so no migration glob ever picks it up — Bootstrap is
-- its only executor (go:embed in accounts.go).
--
-- Replaces the global hunt_jobs.fit_* columns for every account-scoped view:
-- a jobs fit is a JUDGMENT relative to one accounts profile, not a property
-- of the shared hunt_* corpus. hunt_jobs.fit_* stays until the post-soak drop
-- (ADR-13) but is dead to all reads and writes from this phase on.
--
-- job_id deliberately carries NO FK to hunt_jobs: Bootstrap runs BEFORE
-- hStore.Migrate (ADR-6 boot order — the FK direction is accounts→jobs, and
-- on a fresh DB hunt_jobs does not exist yet when this runs). The write path
-- (AccountStore.SetJobScore) enforces job existence with a WHERE EXISTS
-- probe instead. Orphaned rows for a deleted job are inert.
-- score_rationale holds the same JSONB shape hunt_jobs.score_rationale used:
-- {"fit_reasons":[],"fit_gaps":[],"success_reasoning":"..."}.
CREATE TABLE IF NOT EXISTS account_job_scores (
    account_id      UUID NOT NULL REFERENCES panel_accounts(id),
    job_id          BIGINT NOT NULL,
    fit_score       INT,
    fit_band        TEXT,
    success_band    TEXT,
    over_under      TEXT,
    score_rationale JSONB,
    scored_at       TIMESTAMPTZ,
    CONSTRAINT account_job_scores_account_job_key UNIQUE (account_id, job_id)
);
