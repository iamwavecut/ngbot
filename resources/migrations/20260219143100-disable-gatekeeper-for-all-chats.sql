-- +migrate Up
UPDATE chats
SET gatekeeper_enabled = 0;

-- +migrate Down
SELECT 1;
