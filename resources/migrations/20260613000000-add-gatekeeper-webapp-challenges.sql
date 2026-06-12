-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN web_app_token TEXT NOT NULL DEFAULT '';

ALTER TABLE gatekeeper_challenges
ADD COLUMN join_request_query_id TEXT NOT NULL DEFAULT '';

ALTER TABLE gatekeeper_challenges
ADD COLUMN captcha_prompt TEXT NOT NULL DEFAULT '';

ALTER TABLE gatekeeper_challenges
ADD COLUMN captcha_options_json TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_web_app_token
ON gatekeeper_challenges(web_app_token)
WHERE web_app_token <> '';

-- +migrate Down
DROP INDEX IF EXISTS idx_gatekeeper_challenges_web_app_token;

ALTER TABLE gatekeeper_challenges
DROP COLUMN captcha_options_json;

ALTER TABLE gatekeeper_challenges
DROP COLUMN captcha_prompt;

ALTER TABLE gatekeeper_challenges
DROP COLUMN join_request_query_id;

ALTER TABLE gatekeeper_challenges
DROP COLUMN web_app_token;
