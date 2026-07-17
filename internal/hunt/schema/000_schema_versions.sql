-- BH-8: schema version tracking table.
-- Records which migration files have been applied, enabling code/schema
-- drift detection at startup. Each migration inserts its filename here
-- after successful execution.
CREATE TABLE IF NOT EXISTS schema_versions (
    version     TEXT PRIMARY KEY,   -- migration filename (e.g. "001_hunt_bounties.sql")
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
