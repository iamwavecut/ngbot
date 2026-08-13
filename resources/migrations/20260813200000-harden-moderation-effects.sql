-- +migrate Up
ALTER TABLE moderation_action_fences
ADD COLUMN effect_started_at DATETIME;

ALTER TABLE user_restrictions
ADD COLUMN prior_until_date INTEGER NOT NULL DEFAULT 0;

-- +migrate Down
ALTER TABLE user_restrictions DROP COLUMN prior_until_date;
ALTER TABLE moderation_action_fences DROP COLUMN effect_started_at;
