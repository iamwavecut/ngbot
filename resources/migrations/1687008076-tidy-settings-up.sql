-- +migrate Up
DROP TABLE IF EXISTS "charade_scores";
DROP TABLE IF EXISTS "meta";
DROP TABLE IF EXISTS "chat_members";

ALTER TABLE "chats" RENAME TO "chats_old";

CREATE TABLE "chats" (
    "id" INTEGER PRIMARY KEY,
    "enabled" BOOLEAN NOT NULL DEFAULT 1,
    "challenge_timeout" INTEGER NOT NULL DEFAULT 180,
    "reject_timeout" INTEGER NOT NULL DEFAULT 600,
    "language" TEXT NOT NULL DEFAULT 'en'
);
INSERT INTO "chats" ("id", "enabled", "challenge_timeout", "reject_timeout", "language")
SELECT co.id, true, 180, 600, co.language FROM "chats_old" co;
DROP TABLE IF EXISTS "chats_old";

CREATE TABLE IF NOT EXISTS "chat_members" (
    "chat_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    PRIMARY KEY ("chat_id", "user_id"),
    FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

-- +migrate Down
DROP TABLE IF EXISTS "chat_members";
DROP TABLE IF EXISTS "chats";