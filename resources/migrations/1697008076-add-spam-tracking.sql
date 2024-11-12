-- +migrate Up
CREATE TABLE IF NOT EXISTS "user_restrictions" (
    "user_id" BIGINT NOT NULL,
    "chat_id" BIGINT NOT NULL,
    "restricted_at" DATETIME NOT NULL,
    "expires_at" DATETIME NOT NULL,
    "reason" TEXT NOT NULL,
    PRIMARY KEY ("chat_id", "user_id")
);

-- +migrate Down
DROP TABLE IF EXISTS "user_restrictions";
