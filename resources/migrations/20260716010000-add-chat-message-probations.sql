-- +migrate Up
CREATE TABLE chat_message_probations (
	chat_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	started_at DATETIME NOT NULL,
	eligible_at DATETIME NOT NULL,
	graduated_at DATETIME,
	PRIMARY KEY (chat_id, user_id),
	FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
) WITHOUT ROWID;

-- +migrate Down
DROP TABLE chat_message_probations;
