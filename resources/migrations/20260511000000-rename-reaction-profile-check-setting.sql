-- +migrate Up
ALTER TABLE chats
	RENAME COLUMN reaction_moderation_enabled TO reaction_profile_check_enabled;

-- +migrate Down
ALTER TABLE chats
	RENAME COLUMN reaction_profile_check_enabled TO reaction_moderation_enabled;
