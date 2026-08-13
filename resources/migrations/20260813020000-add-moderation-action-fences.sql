-- +migrate Up
CREATE TABLE moderation_action_fences (
    action_key TEXT PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'started', 'banned', 'completed', 'reconciliation')),
    owner TEXT NOT NULL DEFAULT '',
    ban_until DATETIME NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX idx_moderation_action_fences_status_updated
    ON moderation_action_fences (status, updated_at);

-- +migrate Down
DROP INDEX IF EXISTS idx_moderation_action_fences_status_updated;
DROP TABLE IF EXISTS moderation_action_fences;
