-- +migrate Up
DROP TABLE IF EXISTS chats;
CREATE TABLE IF NOT EXISTS "chats"
(
    "id"       BIGINT NOT NULL,
    "title"    TEXT   NOT NULL,
    "language" TEXT,
    PRIMARY KEY ("id")
);

-- +migrate Down
DROP TABLE chats;
