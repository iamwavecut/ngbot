-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN web_app_opened_at TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_unopened_webapp
ON gatekeeper_challenges(created_at)
WHERE web_app_token <> '' AND web_app_opened_at IS NULL;

-- +migrate Down
DROP INDEX IF EXISTS idx_gatekeeper_challenges_unopened_webapp;

ALTER TABLE gatekeeper_challenges
DROP COLUMN web_app_opened_at;
