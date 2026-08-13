-- +migrate Up
ALTER TABLE chats
ADD COLUMN settings_revision INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_chat_challenged_messages_retention
ON chat_challenged_messages(challenged_at, chat_id, user_id, message_id);

CREATE INDEX idx_recent_joiners_retention
ON recent_joiners(processed, joined_at, id);

CREATE INDEX idx_spam_cases_terminal_retention
ON spam_cases(status, resolved_at, id);

-- +migrate Down
DROP INDEX IF EXISTS idx_spam_cases_terminal_retention;
DROP INDEX IF EXISTS idx_recent_joiners_retention;
DROP INDEX IF EXISTS idx_chat_challenged_messages_retention;

ALTER TABLE chats
DROP COLUMN settings_revision;
