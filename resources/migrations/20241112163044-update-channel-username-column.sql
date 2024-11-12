-- +migrate Up
ALTER TABLE spam_cases
	RENAME COLUMN channel_id TO channel_username;

ALTER TABLE spam_cases
	ALTER COLUMN channel_username TYPE text;

-- +migrate Down
ALTER TABLE spam_cases
	ALTER COLUMN channel_username TYPE integer;

ALTER TABLE spam_cases
	RENAME COLUMN channel_username TO channel_id;
