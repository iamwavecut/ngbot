-- +migrate Up
DROP TABLE IF EXISTS charade_scores;
CREATE TABLE IF NOT EXISTS "charade_scores"
(
    "user_id" INT    NOT NULL,
    "chat_id" BIGINT NOT NULL,
    "score"   INT    NOT NULL,
    PRIMARY KEY ("user_id", "chat_id")
);

-- +migrate Down
DROP TABLE charade_scores;
