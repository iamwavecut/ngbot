-- +migrate Up
ALTER TABLE spam_cases
ADD COLUMN resolve_at TIMESTAMP;

ALTER TABLE spam_cases
ADD COLUMN next_attempt_at TIMESTAMP;

ALTER TABLE spam_cases
ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;

ALTER TABLE spam_cases
ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_spam_cases_due_resolution
ON spam_cases(status, resolve_at, next_attempt_at);

CREATE INDEX idx_spam_case_report_messages_due
ON spam_case_report_messages(created_at);

-- +migrate Down
DROP INDEX IF EXISTS idx_spam_case_report_messages_due;
DROP INDEX IF EXISTS idx_spam_cases_due_resolution;

ALTER TABLE spam_cases
DROP COLUMN last_error;

ALTER TABLE spam_cases
DROP COLUMN attempt_count;

ALTER TABLE spam_cases
DROP COLUMN next_attempt_at;

ALTER TABLE spam_cases
DROP COLUMN resolve_at;
