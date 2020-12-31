-- +migrate Up
DROP TABLE IF EXISTS users;
CREATE TABLE IF NOT EXISTS "users"
(
    "id"            BIGINT  NOT NULL,
    "first_name"    TEXT    NOT NULL,
    "last_name"     TEXT    NOT NULL,
    "username"      TEXT    NOT NULL,
    "language_code" TEXT    NOT NULL,
    "is_bot"        TINYINT NOT NULL,

    PRIMARY KEY ("id")
);

-- +migrate Down
DROP TABLE users;
