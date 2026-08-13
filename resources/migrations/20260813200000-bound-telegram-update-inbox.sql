-- +migrate Up
ALTER TABLE telegram_update_inbox ADD COLUMN payload_bytes INTEGER NOT NULL DEFAULT 0 CHECK (payload_bytes >= 0);
ALTER TABLE telegram_update_inbox ADD COLUMN error_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_update_failures ADD COLUMN error_digest TEXT NOT NULL DEFAULT '';

UPDATE telegram_update_inbox
SET payload_bytes = length(payload), last_error = '';

UPDATE telegram_update_failures
SET last_error = '';

CREATE INDEX idx_telegram_update_inbox_pending_usage
ON telegram_update_inbox(status, payload_bytes)
WHERE status IN ('pending', 'processing', 'retry');

CREATE INDEX idx_telegram_update_inbox_dispatch_pending_usage
ON telegram_update_inbox(dispatch_key, status, payload_bytes)
WHERE status IN ('pending', 'processing', 'retry');

-- +migrate Down
DROP INDEX IF EXISTS idx_telegram_update_inbox_dispatch_pending_usage;
DROP INDEX IF EXISTS idx_telegram_update_inbox_pending_usage;
ALTER TABLE telegram_update_failures DROP COLUMN error_digest;
ALTER TABLE telegram_update_inbox DROP COLUMN error_digest;
ALTER TABLE telegram_update_inbox DROP COLUMN payload_bytes;
