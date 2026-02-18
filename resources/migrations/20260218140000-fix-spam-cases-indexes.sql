-- +migrate Up
CREATE INDEX IF NOT EXISTS "idx_spam_cases_chat_user" ON "spam_cases" ("chat_id", "user_id");
CREATE INDEX IF NOT EXISTS "idx_spam_cases_status" ON "spam_cases" ("status");

-- +migrate Down
DROP INDEX IF EXISTS "idx_spam_cases_chat_user";
DROP INDEX IF EXISTS "idx_spam_cases_status";
