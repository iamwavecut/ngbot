-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN user_restricted BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE gatekeeper_challenges
ADD COLUMN notice_message_id INTEGER NOT NULL DEFAULT 0;

UPDATE gatekeeper_challenges
SET user_restricted = TRUE
WHERE comm_chat_id = chat_id;

-- +migrate Down
ALTER TABLE gatekeeper_challenges
DROP COLUMN notice_message_id;

ALTER TABLE gatekeeper_challenges
DROP COLUMN user_restricted;
