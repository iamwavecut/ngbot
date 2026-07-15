-- +migrate Up
CREATE TABLE gatekeeper_dm_fallback_retry_backup (
	challenge_id TEXT PRIMARY KEY,
	next_attempt_at TIMESTAMP,
	attempt_count INTEGER NOT NULL
);

INSERT INTO gatekeeper_dm_fallback_retry_backup (
	challenge_id, next_attempt_at, attempt_count
)
SELECT challenge_id, next_attempt_at, attempt_count
FROM gatekeeper_challenges
WHERE status = 'web_app_fallback_pending'
	AND next_attempt_at IS NULL
	AND (
		UPPER(last_error) LIKE '%BOT CAN''T INITIATE CONVERSATION%'
		OR UPPER(last_error) LIKE '%BOT_CANT_INITIATE_CONVERSATION%'
	);

UPDATE gatekeeper_challenges
SET next_attempt_at = CURRENT_TIMESTAMP,
	attempt_count = 0
WHERE challenge_id IN (
	SELECT challenge_id FROM gatekeeper_dm_fallback_retry_backup
);

-- +migrate Down
UPDATE gatekeeper_challenges
SET next_attempt_at = (
		SELECT backup.next_attempt_at
		FROM gatekeeper_dm_fallback_retry_backup AS backup
		WHERE backup.challenge_id = gatekeeper_challenges.challenge_id
	),
	attempt_count = (
		SELECT backup.attempt_count
		FROM gatekeeper_dm_fallback_retry_backup AS backup
		WHERE backup.challenge_id = gatekeeper_challenges.challenge_id
	)
WHERE challenge_id IN (
	SELECT challenge_id FROM gatekeeper_dm_fallback_retry_backup
);

DROP TABLE gatekeeper_dm_fallback_retry_backup;
