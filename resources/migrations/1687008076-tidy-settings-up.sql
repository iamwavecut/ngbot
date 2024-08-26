-- +migrate Up
DROP TABLE IF EXISTS "charade_scores";
DROP TABLE IF EXISTS "meta";

-- Check if columns exist before dropping
PRAGMA foreign_keys=off;
BEGIN TRANSACTION;

ALTER TABLE "chats" RENAME TO "chats_old";

CREATE TABLE "chats" (
    "id" INTEGER PRIMARY KEY,
    "enabled" BOOLEAN NOT NULL DEFAULT 1,
    "migrated" BOOLEAN NOT NULL DEFAULT 0,
    "challenge_timeout" INTEGER NOT NULL DEFAULT 180,  -- 3 minutes in seconds
    "reject_timeout" INTEGER NOT NULL DEFAULT 600  -- 10 minutes in seconds
);

INSERT INTO "chats" (id, enabled, migrated, challenge_timeout, reject_timeout)
SELECT id, 1, 0, 180, 600
FROM "chats_old";

DROP TABLE "chats_old";

PRAGMA foreign_keys=on;
COMMIT;

CREATE TABLE IF NOT EXISTS "chat_members" (
    "chat_id" INTEGER NOT NULL,
    "user_id" INTEGER NOT NULL,
    PRIMARY KEY ("chat_id", "user_id"),
    FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

-- +migrate Down
DROP TABLE IF EXISTS "chat_members";

PRAGMA foreign_keys=off;
BEGIN TRANSACTION;

ALTER TABLE "chats" RENAME TO "chats_old";

CREATE TABLE "chats" (
    "id" INTEGER PRIMARY KEY,
    "title" TEXT,
    "type" TEXT
);

INSERT INTO "chats" (id, title, type)
SELECT id, NULL, NULL
FROM "chats_old";

DROP TABLE "chats_old";

PRAGMA foreign_keys=on;
COMMIT;
