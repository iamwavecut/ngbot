-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN user_language TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE gatekeeper_challenges
DROP COLUMN user_language;
