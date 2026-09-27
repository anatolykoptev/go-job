-- mcp_api_keys.sql — per-account MCP bearer keys (plan ADR-3). Accounts-owned
-- DDL applied inside accounts.Bootstrap, right after EnsureSchema creates the
-- panel_accounts this table FK-references — NEVER via a standard migration
-- root: CI psql-applies internal/{engine/jobs,hunt}/schema/*.sql standalone
-- (no EnsureSchema), so an account-FK table must migrate with the schema it
-- references. Deliberately kept OUT of any */schema/ directory so no
-- migration glob ever picks it up — Bootstrap is its only executor
-- (go:embed in accounts.go). The same rule binds every future account-FK
-- table: ADR-6 made self-contained.
--
-- key_hash = sha256(raw bearer token): the edge verifier hashes the presented
-- token and joins panel_accounts on EVERY request, so revocation (revoked_at)
-- and account deactivation take effect immediately — no cache, no reload.
-- key_prefix holds the first 8 chars of the raw token for operator
-- identification in logs/UI; the raw token itself is never persisted.
-- Managed by gojob-admin (p1-gojob-admin-cli) and the MCP_LEGACY_TOKEN_SEED
-- boot seed. All statements are idempotent — Bootstrap runs on every boot.
CREATE TABLE IF NOT EXISTS mcp_api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Deliberate NO ACTION (sibling panel_totp_recovery_codes uses CASCADE):
    -- accounts are deactivated (active=false), never deleted — a key row must
    -- never outlive or silently detach from its owner, so the FK stays strict.
    account_id   UUID NOT NULL REFERENCES panel_accounts(id),
    key_hash     BYTEA NOT NULL UNIQUE,
    key_prefix   TEXT NOT NULL,
    label        TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
-- Per-account key listing (the CLI key-list verb) only ever wants live keys.
CREATE INDEX IF NOT EXISTS idx_mcp_api_keys_account_active
    ON mcp_api_keys (account_id) WHERE revoked_at IS NULL;
