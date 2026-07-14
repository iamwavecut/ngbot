-- +migrate Up
CREATE TABLE durable_spam_legacy_recovery_backup (
    id INTEGER PRIMARY KEY,
    status TEXT NOT NULL,
    resolved_at DATETIME,
    resolve_at TIMESTAMP,
    next_attempt_at TIMESTAMP,
    attempt_count INTEGER NOT NULL,
    last_error TEXT NOT NULL
);

INSERT INTO durable_spam_legacy_recovery_backup (
    id, status, resolved_at, resolve_at, next_attempt_at, attempt_count, last_error
)
SELECT id, status, resolved_at, resolve_at, next_attempt_at, attempt_count, last_error
FROM spam_cases
WHERE message_id = 0
  AND resolved_at IS NULL
  AND status IN ('pending', 'resolving_false_positive');

UPDATE spam_cases
SET status = 'false_positive',
    resolved_at = CURRENT_TIMESTAMP,
    next_attempt_at = NULL
WHERE id IN (SELECT id FROM durable_spam_legacy_recovery_backup);

-- +migrate Down
UPDATE spam_cases
SET status = (
        SELECT status
        FROM durable_spam_legacy_recovery_backup
        WHERE durable_spam_legacy_recovery_backup.id = spam_cases.id
    ),
    resolved_at = (
        SELECT resolved_at
        FROM durable_spam_legacy_recovery_backup
        WHERE durable_spam_legacy_recovery_backup.id = spam_cases.id
    ),
    resolve_at = (
        SELECT resolve_at
        FROM durable_spam_legacy_recovery_backup
        WHERE durable_spam_legacy_recovery_backup.id = spam_cases.id
    ),
    next_attempt_at = (
        SELECT next_attempt_at
        FROM durable_spam_legacy_recovery_backup
        WHERE durable_spam_legacy_recovery_backup.id = spam_cases.id
    ),
    attempt_count = (
        SELECT attempt_count
        FROM durable_spam_legacy_recovery_backup
        WHERE durable_spam_legacy_recovery_backup.id = spam_cases.id
    ),
    last_error = (
        SELECT last_error
        FROM durable_spam_legacy_recovery_backup
        WHERE durable_spam_legacy_recovery_backup.id = spam_cases.id
    )
WHERE id IN (SELECT id FROM durable_spam_legacy_recovery_backup);

DROP TABLE durable_spam_legacy_recovery_backup;
