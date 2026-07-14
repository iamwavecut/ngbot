-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN challenge_id TEXT NOT NULL DEFAULT '';

ALTER TABLE gatekeeper_challenges
ADD COLUMN next_attempt_at TIMESTAMP;

ALTER TABLE gatekeeper_challenges
ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;

ALTER TABLE gatekeeper_challenges
ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

UPDATE gatekeeper_challenges
SET challenge_id = lower(hex(randomblob(16)))
WHERE challenge_id = '';

CREATE UNIQUE INDEX idx_gatekeeper_challenges_challenge_id
ON gatekeeper_challenges(challenge_id);

CREATE INDEX idx_gatekeeper_challenges_due
ON gatekeeper_challenges(status, next_attempt_at);

-- +migrate Down
DROP INDEX IF EXISTS idx_gatekeeper_challenges_due;
DROP INDEX IF EXISTS idx_gatekeeper_challenges_challenge_id;

ALTER TABLE gatekeeper_challenges
DROP COLUMN last_error;

ALTER TABLE gatekeeper_challenges
DROP COLUMN attempt_count;

ALTER TABLE gatekeeper_challenges
DROP COLUMN next_attempt_at;

ALTER TABLE gatekeeper_challenges
DROP COLUMN challenge_id;
