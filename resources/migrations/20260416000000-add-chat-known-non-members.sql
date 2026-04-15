-- +migrate Up
CREATE TABLE IF NOT EXISTS chat_known_non_members (
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    PRIMARY KEY (chat_id, user_id)
);

-- +migrate Down
DROP TABLE IF EXISTS chat_known_non_members;
