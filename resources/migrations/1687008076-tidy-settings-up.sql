-- +migrate Up
DROP TABLE IF EXISTS "charade_scores";
DROP TABLE IF EXISTS "meta";
DROP TABLE IF EXISTS "chat_members";
DROP TABLE IF EXISTS "chats";

CREATE TABLE "chats" (
    "id" INTEGER PRIMARY KEY,
    "enabled" BOOLEAN NOT NULL DEFAULT 1,
    "migrated" BOOLEAN NOT NULL DEFAULT 0,
    "challenge_timeout" INTEGER NOT NULL DEFAULT 180,  -- 3 minutes in seconds
    "reject_timeout" INTEGER NOT NULL DEFAULT 600  -- 10 minutes in seconds
    "language" TEXT NOT NULL DEFAULT 'en'
);

CREATE TABLE IF NOT EXISTS "chat_members" (
    "chat_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    PRIMARY KEY ("chat_id", "user_id"),
    FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

-- +migrate Down
-- no-op
SELECT 1;