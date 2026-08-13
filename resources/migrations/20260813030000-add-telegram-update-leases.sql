-- +migrate Up
ALTER TABLE telegram_update_inbox ADD COLUMN lease_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE telegram_update_inbox ADD COLUMN lease_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telegram_update_inbox ADD COLUMN lease_until TIMESTAMP;

UPDATE telegram_update_inbox
SET lease_until = CURRENT_TIMESTAMP
WHERE status = 'processing';

CREATE INDEX idx_telegram_update_inbox_processing_lease
ON telegram_update_inbox(status, lease_until);

-- +migrate Down
DROP INDEX IF EXISTS idx_telegram_update_inbox_processing_lease;
ALTER TABLE telegram_update_inbox DROP COLUMN lease_until;
ALTER TABLE telegram_update_inbox DROP COLUMN lease_version;
ALTER TABLE telegram_update_inbox DROP COLUMN lease_owner;
