-- +migrate Up
CREATE TABLE IF NOT EXISTS gatekeeper_challenges (
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

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_expires_at
ON gatekeeper_challenges(expires_at);

-- +migrate Down
DROP TABLE IF EXISTS gatekeeper_challenges;
