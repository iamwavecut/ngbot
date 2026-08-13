-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN action_owner TEXT NOT NULL DEFAULT '';

ALTER TABLE gatekeeper_challenges
ADD COLUMN action_lease_until TIMESTAMP;

ALTER TABLE gatekeeper_challenges
ADD COLUMN action_version INTEGER NOT NULL DEFAULT 0;

ALTER TABLE gatekeeper_challenges
ADD COLUMN action_phase TEXT NOT NULL DEFAULT 'ready';

ALTER TABLE gatekeeper_challenges
ADD COLUMN effect_started_at TIMESTAMP;

ALTER TABLE gatekeeper_challenges
ADD COLUMN cancel_requested BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_gatekeeper_challenges_action_lease
ON gatekeeper_challenges(status, next_attempt_at, action_lease_until, action_phase);

CREATE TABLE gatekeeper_challenge_reconciliations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	challenge_id TEXT NOT NULL,
	comm_chat_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	action_status TEXT NOT NULL,
	action_phase TEXT NOT NULL DEFAULT 'ready',
	artifact_message_id INTEGER NOT NULL DEFAULT 0,
	challenge_message_id INTEGER NOT NULL DEFAULT 0,
	join_message_id INTEGER NOT NULL DEFAULT 0,
	notice_message_id INTEGER NOT NULL DEFAULT 0,
	join_request_query_present BOOLEAN NOT NULL DEFAULT FALSE,
	web_app_token_present BOOLEAN NOT NULL DEFAULT FALSE,
	user_restricted BOOLEAN NOT NULL DEFAULT FALSE,
	attempt_count INTEGER NOT NULL,
	last_error TEXT NOT NULL,
	challenge_created_at TIMESTAMP NOT NULL,
	expires_at TIMESTAMP NOT NULL,
	effect_started_at TIMESTAMP,
	reconciliation_due_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	retention_until TIMESTAMP NOT NULL DEFAULT (datetime('now', '+30 days')),
	resolution_status TEXT NOT NULL DEFAULT 'pending',
	resolution TEXT NOT NULL DEFAULT '',
	resolved_at TIMESTAMP,
	version INTEGER NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX idx_gatekeeper_challenge_reconciliations_challenge
ON gatekeeper_challenge_reconciliations(challenge_id);

CREATE INDEX idx_gatekeeper_challenge_reconciliations_due
ON gatekeeper_challenge_reconciliations(resolution_status, reconciliation_due_at, action_status);

INSERT INTO gatekeeper_challenge_reconciliations (
	challenge_id, comm_chat_id, user_id, chat_id, action_status, action_phase,
	challenge_message_id, join_message_id, notice_message_id,
	join_request_query_present, web_app_token_present, user_restricted,
	attempt_count, last_error, challenge_created_at, expires_at
)
SELECT challenge_id, comm_chat_id, user_id, chat_id, status, 'ready',
	challenge_message_id, join_message_id, notice_message_id,
	join_request_query_id <> '', web_app_token <> '', user_restricted,
	attempt_count, last_error, created_at, expires_at
FROM gatekeeper_challenges
WHERE next_attempt_at IS NULL
	AND status IN ('restrict_pending', 'web_app_fallback_pending', 'approve_query_pending', 'approve_member_pending', 'unrestrict_pending', 'reject_pending')
	AND (
		upper(last_error) LIKE '%QUERY_ID_INVALID%'
		OR upper(last_error) LIKE '%QUERY IS TOO OLD%'
		OR upper(last_error) LIKE '%BOT CAN''T INITIATE CONVERSATION%'
		OR upper(last_error) LIKE '%BOT_CANT_INITIATE_CONVERSATION%'
		OR upper(last_error) LIKE '%BOT WAS BLOCKED BY THE USER%'
		OR upper(last_error) LIKE '%USER IS DEACTIVATED%'
	);

DELETE FROM gatekeeper_challenges
WHERE challenge_id IN (SELECT challenge_id FROM gatekeeper_challenge_reconciliations);

UPDATE gatekeeper_challenges
SET next_attempt_at = CURRENT_TIMESTAMP,
	action_phase = 'ready'
WHERE next_attempt_at IS NULL
	AND status IN ('restrict_pending', 'web_app_fallback_pending', 'approve_query_pending', 'approve_member_pending', 'unrestrict_pending', 'reject_pending');

-- +migrate Down
INSERT OR IGNORE INTO gatekeeper_challenges (
	challenge_id, comm_chat_id, user_id, chat_id, status,
	join_message_id, challenge_message_id, notice_message_id,
	created_at, expires_at, next_attempt_at, attempt_count, last_error, user_restricted
)
SELECT challenge_id, comm_chat_id, user_id, chat_id, action_status,
	join_message_id, challenge_message_id, notice_message_id,
	challenge_created_at, expires_at, NULL, attempt_count, last_error, user_restricted
FROM gatekeeper_challenge_reconciliations
WHERE resolution_status = 'pending';

DROP INDEX IF EXISTS idx_gatekeeper_challenge_reconciliations_due;
DROP INDEX IF EXISTS idx_gatekeeper_challenge_reconciliations_challenge;
DROP TABLE IF EXISTS gatekeeper_challenge_reconciliations;

DROP INDEX IF EXISTS idx_gatekeeper_challenges_action_lease;

ALTER TABLE gatekeeper_challenges
DROP COLUMN cancel_requested;

ALTER TABLE gatekeeper_challenges
DROP COLUMN effect_started_at;

ALTER TABLE gatekeeper_challenges
DROP COLUMN action_phase;

ALTER TABLE gatekeeper_challenges
DROP COLUMN action_version;

ALTER TABLE gatekeeper_challenges
DROP COLUMN action_lease_until;

ALTER TABLE gatekeeper_challenges
DROP COLUMN action_owner;
