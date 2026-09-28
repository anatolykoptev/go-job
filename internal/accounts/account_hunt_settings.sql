-- account_hunt_settings.sql — per-account hunt worker settings (plan ADR-7).
-- Accounts-owned DDL applied inside accounts.Bootstrap, right after
-- EnsureSchema creates the panel_accounts this table PK-references — same
-- rule as mcp_api_keys.sql and account_job_scores.sql: NEVER a standard
-- migration root (CI psql-applies */schema/*.sql standalone, before any
-- EnsureSchema). Kept OUT of every */schema/ directory so no migration glob
-- ever picks it up — Bootstrap is its only executor (go:embed in
-- accounts.go).
--
-- Replaces the single-row hunt_settings table (hunt schema 013, id=1 CHECK)
-- for every account-scoped knob: enabled/queries/notify_*/score_* are
-- per-account now; a missing row means DISABLED — provisioning never
-- silently arms a hunt. The fleet-global knobs stay outside per-account
-- reach by design and are env/deploy config only:
--   - cycle interval       → HUNT_INGEST_INTERVAL (one ticker for the fleet)
--   - fleet LLM cap        → HUNT_SCORE_MAX_LLM_PER_CYCLE (cross-account
--                            budget; score_max_llm_per_cycle below can only
--                            tighten it per account)
--   - sweep limit          → HUNT_SCORE_SWEEP_LIMIT
-- hunt_settings stays until the post-soak drop (ADR-13); Bootstrap's
-- BackfillLegacyHuntSettings copies row id=1 into the operator's row once.
--
-- score_max_llm_per_cycle is NULLABLE: NULL = no per-account sub-cap, the
-- account shares the fleet cap. score_fail_open defaults FALSE for new
-- accounts (Jaccard-as-fit on LLM error is silent data pollution — the
-- migrated operator row preserves its stored value instead).
CREATE TABLE IF NOT EXISTS account_hunt_settings (
    account_id              UUID PRIMARY KEY REFERENCES panel_accounts(id),
    enabled                 BOOLEAN  NOT NULL DEFAULT false,
    queries                 TEXT     NOT NULL DEFAULT '',
    notify_chat_id          BIGINT   NOT NULL DEFAULT 0,   -- 0 = no Telegram notify
    notify_min_fit          INT      NOT NULL DEFAULT 0 CHECK (notify_min_fit BETWEEN 0 AND 100),
    notify_max_age_seconds  INT      NOT NULL DEFAULT 172800, -- 48h
    score_enabled           BOOLEAN  NOT NULL DEFAULT false,
    score_min_jaccard       INT      NOT NULL DEFAULT 8,
    score_max_llm_per_cycle INT      NULL,                 -- NULL = fleet cap only
    score_fail_open         BOOLEAN  NOT NULL DEFAULT false,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
