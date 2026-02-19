-- +migrate Up
CREATE TABLE IF NOT EXISTS gatekeeper_challenges_new (
    comm_chat_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    success_uuid TEXT NOT NULL,
    join_message_id INTEGER NOT NULL DEFAULT 0,
    challenge_message_id INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    PRIMARY KEY (comm_chat_id, user_id, chat_id)
);

INSERT INTO gatekeeper_challenges_new (
    comm_chat_id,
    user_id,
    chat_id,
    success_uuid,
    join_message_id,
    challenge_message_id,
    attempts,
    created_at,
    expires_at
)
SELECT
    comm_chat_id,
    user_id,
    chat_id,
    success_uuid,
    join_message_id,
    challenge_message_id,
    attempts,
    created_at,
    expires_at
FROM gatekeeper_challenges;

DROP TABLE gatekeeper_challenges;
ALTER TABLE gatekeeper_challenges_new RENAME TO gatekeeper_challenges;

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_expires_at
ON gatekeeper_challenges(expires_at);

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_message_lookup
ON gatekeeper_challenges(comm_chat_id, user_id, challenge_message_id);

-- +migrate Down
CREATE TABLE IF NOT EXISTS gatekeeper_challenges_old (
    comm_chat_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL,
    success_uuid TEXT NOT NULL,
    join_message_id INTEGER NOT NULL DEFAULT 0,
    challenge_message_id INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    PRIMARY KEY (comm_chat_id, user_id)
);

INSERT INTO gatekeeper_challenges_old (
    comm_chat_id,
    user_id,
    chat_id,
    success_uuid,
    join_message_id,
    challenge_message_id,
    attempts,
    created_at,
    expires_at
)
SELECT
    comm_chat_id,
    user_id,
    chat_id,
    success_uuid,
    join_message_id,
    challenge_message_id,
    attempts,
    created_at,
    expires_at
FROM gatekeeper_challenges;

DROP TABLE gatekeeper_challenges;
ALTER TABLE gatekeeper_challenges_old RENAME TO gatekeeper_challenges;

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_expires_at
ON gatekeeper_challenges(expires_at);
