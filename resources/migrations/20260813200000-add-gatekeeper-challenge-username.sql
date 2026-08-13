-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN username TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE gatekeeper_challenges
DROP COLUMN username;
