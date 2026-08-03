-- 013_hunt_settings.sql — operator-tunable hunt worker settings stored in DB.
-- Single-row table (id=1 CHECK) edited via the admin UI; replaces env-only
-- configuration for the hunt ingest worker. Env vars remain as fallback
-- defaults when the row is absent or a column is zero/unset.
CREATE TABLE IF NOT EXISTS hunt_settings (
    id                    SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled               BOOLEAN  NOT NULL DEFAULT true,
    interval_seconds      INT      NOT NULL DEFAULT 21600,  -- 6h
    queries               TEXT     NOT NULL DEFAULT 'software engineer,backend engineer,golang developer',
    notify_chat_id        BIGINT   NOT NULL DEFAULT 0,      -- 0 = no Telegram notify
    notify_min_fit        INT      NOT NULL DEFAULT 0 CHECK (notify_min_fit BETWEEN 0 AND 100),
    notify_max_age_seconds INT     NOT NULL DEFAULT 172800, -- 48h
    score_enabled         BOOLEAN  NOT NULL DEFAULT true,
    score_min_jaccard     INT      NOT NULL DEFAULT 8,
    score_max_llm_per_cycle INT    NOT NULL DEFAULT 50,
    score_sweep_limit     INT      NOT NULL DEFAULT 50,
    score_fail_open       BOOLEAN  NOT NULL DEFAULT true,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO hunt_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
