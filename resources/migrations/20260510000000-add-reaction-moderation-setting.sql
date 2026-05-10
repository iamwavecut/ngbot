-- +migrate Up
ALTER TABLE chats
ADD COLUMN reaction_moderation_enabled BOOLEAN NOT NULL DEFAULT 1;

-- +migrate Down
CREATE TABLE chats_old (
    id INTEGER PRIMARY KEY,
    language TEXT NOT NULL DEFAULT 'en',
    enabled BOOLEAN NOT NULL DEFAULT 1,
    gatekeeper_enabled BOOLEAN NOT NULL DEFAULT 0,
    gatekeeper_captcha_enabled BOOLEAN NOT NULL DEFAULT 0,
    gatekeeper_greeting_enabled BOOLEAN NOT NULL DEFAULT 0,
    gatekeeper_captcha_options_count INTEGER NOT NULL DEFAULT 5,
    gatekeeper_greeting_text TEXT NOT NULL DEFAULT '',
    llm_first_message_enabled BOOLEAN NOT NULL DEFAULT 1,
    community_voting_enabled BOOLEAN NOT NULL DEFAULT 1,
    community_voting_timeout_override_ns INTEGER NOT NULL DEFAULT -1,
    community_voting_min_voters_override INTEGER NOT NULL DEFAULT -1,
    community_voting_max_voters_override INTEGER NOT NULL DEFAULT -1,
    community_voting_min_voters_percent_override INTEGER NOT NULL DEFAULT -1,
    challenge_timeout INTEGER NOT NULL DEFAULT 300,
    reject_timeout INTEGER NOT NULL DEFAULT 600
);

INSERT INTO chats_old (
    id,
    language,
    enabled,
    gatekeeper_enabled,
    gatekeeper_captcha_enabled,
    gatekeeper_greeting_enabled,
    gatekeeper_captcha_options_count,
    gatekeeper_greeting_text,
    llm_first_message_enabled,
    community_voting_enabled,
    community_voting_timeout_override_ns,
    community_voting_min_voters_override,
    community_voting_max_voters_override,
    community_voting_min_voters_percent_override,
    challenge_timeout,
    reject_timeout
)
SELECT
    id,
    language,
    enabled,
    gatekeeper_enabled,
    gatekeeper_captcha_enabled,
    gatekeeper_greeting_enabled,
    gatekeeper_captcha_options_count,
    gatekeeper_greeting_text,
    llm_first_message_enabled,
    community_voting_enabled,
    community_voting_timeout_override_ns,
    community_voting_min_voters_override,
    community_voting_max_voters_override,
    community_voting_min_voters_percent_override,
    challenge_timeout,
    reject_timeout
FROM chats;

DROP TABLE chats;
ALTER TABLE chats_old RENAME TO chats;
