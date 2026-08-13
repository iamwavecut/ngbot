-- +migrate Up
CREATE TABLE telegram_update_inbox (
	update_id INTEGER PRIMARY KEY,
	dispatch_key TEXT NOT NULL,
	payload BLOB NOT NULL,
	security_relevant BOOLEAN NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'retry', 'completed', 'dead_letter')),
	attempt_count INTEGER NOT NULL DEFAULT 0,
	available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	started_at TIMESTAMP,
	completed_at TIMESTAMP,
	last_error TEXT NOT NULL DEFAULT '',
	outcome_source TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_telegram_update_inbox_runnable
ON telegram_update_inbox(status, available_at, update_id);

CREATE INDEX idx_telegram_update_inbox_dispatch_order
ON telegram_update_inbox(dispatch_key, update_id, status);

CREATE INDEX idx_telegram_update_inbox_retention
ON telegram_update_inbox(status, completed_at);

CREATE TABLE telegram_update_failures (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	update_id INTEGER NOT NULL UNIQUE REFERENCES telegram_update_inbox(update_id) ON DELETE CASCADE,
	dispatch_key TEXT NOT NULL,
	security_relevant BOOLEAN NOT NULL,
	attempt_count INTEGER NOT NULL,
	failure_source TEXT NOT NULL,
	failure_reason TEXT NOT NULL,
	last_error TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	resolved_at TIMESTAMP
);

CREATE INDEX idx_telegram_update_failures_unresolved
ON telegram_update_failures(resolved_at, created_at, update_id);

-- +migrate Down
DROP INDEX IF EXISTS idx_telegram_update_failures_unresolved;
DROP TABLE IF EXISTS telegram_update_failures;
DROP INDEX IF EXISTS idx_telegram_update_inbox_retention;
DROP INDEX IF EXISTS idx_telegram_update_inbox_dispatch_order;
DROP INDEX IF EXISTS idx_telegram_update_inbox_runnable;
DROP TABLE IF EXISTS telegram_update_inbox;
