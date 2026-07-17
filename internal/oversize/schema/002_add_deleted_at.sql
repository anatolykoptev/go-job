-- BH-9: soft delete column to prevent purge racing with active reads.
-- Purge sets deleted_at=NOW() instead of hard DELETE; Get filters
-- deleted_at IS NULL; a separate hard purge deletes rows with
-- deleted_at older than 24h.
ALTER TABLE oversize_responses ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_oversize_responses_deleted_at
    ON oversize_responses (deleted_at)
    WHERE deleted_at IS NOT NULL;
