-- +migrate Up
ALTER TABLE chat_challenged_messages
ADD COLUMN author_kind TEXT NOT NULL DEFAULT 'user';

ALTER TABLE spam_cases
ADD COLUMN author_kind TEXT NOT NULL DEFAULT 'user';

CREATE INDEX idx_spam_cases_author_status
ON spam_cases(chat_id, author_kind, user_id, status);

CREATE TABLE chat_author_trust (
	chat_id INTEGER NOT NULL,
	author_kind TEXT NOT NULL,
	author_id INTEGER NOT NULL,
	safe_messages INTEGER NOT NULL DEFAULT 0 CHECK (safe_messages >= 0),
	trusted_until DATETIME,
	PRIMARY KEY (chat_id, author_kind, author_id),
	CHECK ((author_kind = 'user' AND author_id > 0) OR (author_kind = 'sender_chat' AND author_id < 0)),
	FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
) WITHOUT ROWID;

INSERT INTO chat_author_trust (chat_id, author_kind, author_id, safe_messages, trusted_until)
SELECT legacy.chat_id, 'user', legacy.user_id, 3, DATETIME('now', '+30 days')
FROM (
	SELECT chat_id, user_id FROM chat_members
	UNION
	SELECT chat_id, user_id FROM chat_message_probations WHERE graduated_at IS NOT NULL
) AS legacy
WHERE legacy.user_id > 0
	AND NOT EXISTS (
		SELECT 1 FROM chat_message_probations AS probation
		WHERE probation.chat_id = legacy.chat_id AND probation.user_id = legacy.user_id
			AND probation.graduated_at IS NULL
	);

CREATE TABLE chat_message_context (
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	thread_id INTEGER NOT NULL DEFAULT 0,
	reply_to_message_id INTEGER NOT NULL DEFAULT 0,
	author_kind TEXT NOT NULL,
	author_id INTEGER NOT NULL,
	text TEXT NOT NULL,
	sent_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	update_id INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (chat_id, message_id),
	CHECK ((author_kind = 'user' AND author_id > 0) OR (author_kind = 'sender_chat' AND author_id < 0)),
	FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX idx_chat_message_context_recent
ON chat_message_context(chat_id, thread_id, message_id DESC);

CREATE INDEX idx_chat_message_context_author
ON chat_message_context(chat_id, author_kind, author_id);

CREATE INDEX idx_chat_message_context_retention
ON chat_message_context(sent_at, chat_id, message_id);

CREATE TABLE chat_message_context_tombstones (
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	deleted_at DATETIME NOT NULL,
	PRIMARY KEY (chat_id, message_id),
	FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX idx_chat_message_context_tombstones_retention
ON chat_message_context_tombstones(deleted_at, chat_id, message_id);

-- +migrate Down
CREATE TEMP TABLE comment_author_rollback_guard (
	non_user_rows INTEGER NOT NULL CHECK (non_user_rows = 0)
);

INSERT INTO comment_author_rollback_guard (non_user_rows)
SELECT
	(SELECT COUNT(*) FROM spam_cases WHERE author_kind != 'user') +
	(SELECT COUNT(*) FROM chat_challenged_messages WHERE author_kind != 'user') +
	(SELECT COUNT(*) FROM gatekeeper_challenges WHERE action_phase = 'reject_context_pending');

DROP TABLE comment_author_rollback_guard;
DROP TABLE chat_message_context_tombstones;
DROP TABLE chat_message_context;
DROP TABLE chat_author_trust;
DROP INDEX idx_spam_cases_author_status;

ALTER TABLE spam_cases DROP COLUMN author_kind;
ALTER TABLE chat_challenged_messages DROP COLUMN author_kind;
