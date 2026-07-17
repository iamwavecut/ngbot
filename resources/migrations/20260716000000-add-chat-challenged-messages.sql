-- +migrate Up
CREATE TABLE chat_challenged_messages (
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	challenged_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (chat_id, message_id),
	FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX idx_chat_challenged_messages_user
	ON chat_challenged_messages (chat_id, user_id);

-- +migrate Down
DROP TABLE chat_challenged_messages;
