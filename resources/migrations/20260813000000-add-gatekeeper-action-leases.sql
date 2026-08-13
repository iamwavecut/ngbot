-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN action_owner TEXT NOT NULL DEFAULT '';

ALTER TABLE gatekeeper_challenges
ADD COLUMN action_lease_until TIMESTAMP;

CREATE INDEX idx_gatekeeper_challenges_action_lease
ON gatekeeper_challenges(status, next_attempt_at, action_lease_until);

CREATE TABLE gatekeeper_challenge_reconciliations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	challenge_id TEXT NOT NULL,
	comm_chat_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	action_status TEXT NOT NULL,
	join_request_query_id TEXT NOT NULL DEFAULT '',
	user_restricted BOOLEAN NOT NULL DEFAULT FALSE,
	attempt_count INTEGER NOT NULL,
	last_error TEXT NOT NULL,
	challenge_created_at TIMESTAMP NOT NULL,
	reconciliation_due_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_gatekeeper_challenge_reconciliations_challenge
ON gatekeeper_challenge_reconciliations(challenge_id);

CREATE INDEX idx_gatekeeper_challenge_reconciliations_due
ON gatekeeper_challenge_reconciliations(reconciliation_due_at, action_status);

-- +migrate Down
DROP INDEX IF EXISTS idx_gatekeeper_challenge_reconciliations_due;
DROP INDEX IF EXISTS idx_gatekeeper_challenge_reconciliations_challenge;
DROP TABLE IF EXISTS gatekeeper_challenge_reconciliations;

DROP INDEX IF EXISTS idx_gatekeeper_challenges_action_lease;

ALTER TABLE gatekeeper_challenges
DROP COLUMN action_lease_until;

ALTER TABLE gatekeeper_challenges
DROP COLUMN action_owner;
