-- +migrate Up
ALTER TABLE spam_cases
	ADD COLUMN message_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE spam_cases
	ADD COLUMN pre_vote_restricted BOOLEAN NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS idx_spam_cases_chat_user_message
	ON spam_cases (chat_id, user_id, message_id);

CREATE TABLE IF NOT EXISTS spam_case_report_messages (
	case_id BIGINT NOT NULL,
	chat_id BIGINT NOT NULL,
	message_id INTEGER NOT NULL,
	created_at DATETIME NOT NULL,
	PRIMARY KEY (case_id, chat_id, message_id),
	FOREIGN KEY (case_id) REFERENCES spam_cases (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_spam_case_report_messages_case
	ON spam_case_report_messages (case_id);

UPDATE chats
SET community_voting_enabled = 1;

-- +migrate Down
DROP INDEX IF EXISTS idx_spam_case_report_messages_case;
DROP TABLE IF EXISTS spam_case_report_messages;
DROP INDEX IF EXISTS idx_spam_cases_chat_user_message;

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

CREATE INDEX IF NOT EXISTS idx_spam_cases_chat_user ON spam_cases (chat_id, user_id);
CREATE INDEX IF NOT EXISTS idx_spam_cases_status ON spam_cases (status);
