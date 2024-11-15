-- +migrate Up
CREATE TABLE IF NOT EXISTS recent_joiners (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    join_message_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    username TEXT,
    joined_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    processed BOOLEAN NOT NULL DEFAULT FALSE,
    is_spammer BOOLEAN NULL,
    UNIQUE(chat_id, user_id)
);

CREATE TABLE IF NOT EXISTS banlist (
    user_id INTEGER PRIMARY KEY
);

-- +migrate Down
DROP TABLE recent_joiners;
DROP TABLE banlist;
