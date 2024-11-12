-- +migrate Up
CREATE TABLE IF NOT EXISTS "spam_cases" (
    "id" INTEGER PRIMARY KEY AUTOINCREMENT,
    "chat_id" BIGINT NOT NULL,
    "user_id" BIGINT NOT NULL,
    "message_text" TEXT NOT NULL,
    "created_at" DATETIME NOT NULL,
    "channel_post_id" INTEGER,
    "notification_message_id" INTEGER,
    "status" TEXT NOT NULL DEFAULT 'pending',
    "resolved_at" DATETIME
);

CREATE INDEX IF NOT EXISTS "idx_spam_cases_chat_user" ON "spam_cases" ("chat_id", "user_id");
CREATE INDEX IF NOT EXISTS "idx_spam_cases_status" ON "spam_cases" ("status");

-- +migrate Down
DROP TABLE IF EXISTS "spam_cases";
