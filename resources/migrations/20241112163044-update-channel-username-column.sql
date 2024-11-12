-- +migrate Up
ALTER TABLE spam_cases
	RENAME COLUMN channel_id TO channel_username;

CREATE TABLE spam_cases_new (
	id INTEGER PRIMARY KEY,
	chat_id INTEGER,
	user_id INTEGER,
	message_text TEXT,
	created_at DATETIME,
	channel_username TEXT,
	channel_post_id INTEGER,
	notification_message_id INTEGER,
	status TEXT,
	resolved_at DATETIME
);

INSERT INTO spam_cases_new (id, chat_id, user_id, message_text, created_at, channel_username, channel_post_id, notification_message_id, status, resolved_at)
SELECT id, chat_id, user_id, message_text, created_at, channel_username, channel_post_id, notification_message_id, status, resolved_at
FROM spam_cases;

DROP TABLE spam_cases;

ALTER TABLE spam_cases_new RENAME TO spam_cases;

-- +migrate Down
ALTER TABLE spam_cases
	RENAME COLUMN channel_username TO channel_id;

CREATE TABLE spam_cases_new (
	id INTEGER PRIMARY KEY,
	chat_id INTEGER,
	user_id INTEGER,
	message_text TEXT,
	created_at DATETIME,
	channel_id INTEGER,
	channel_post_id INTEGER,
	notification_message_id INTEGER,
	status TEXT,
	resolved_at DATETIME
);

INSERT INTO spam_cases_new (id, chat_id, user_id, message_text, created_at, channel_id, channel_post_id, notification_message_id, status, resolved_at)
SELECT id, chat_id, user_id, message_text, created_at, channel_id, channel_post_id, notification_message_id, status, resolved_at
FROM spam_cases;

DROP TABLE spam_cases;

ALTER TABLE spam_cases_new RENAME TO spam_cases;
