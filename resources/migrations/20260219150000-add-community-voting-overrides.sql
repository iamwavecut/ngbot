-- +migrate Up
ALTER TABLE chats ADD COLUMN community_voting_timeout_override_ns INTEGER NOT NULL DEFAULT -1;
ALTER TABLE chats ADD COLUMN community_voting_min_voters_override INTEGER NOT NULL DEFAULT -1;
ALTER TABLE chats ADD COLUMN community_voting_max_voters_override INTEGER NOT NULL DEFAULT -1;
ALTER TABLE chats ADD COLUMN community_voting_min_voters_percent_override INTEGER NOT NULL DEFAULT -1;

-- +migrate Down
SELECT 1;
