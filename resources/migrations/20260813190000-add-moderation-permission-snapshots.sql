-- +migrate Up
ALTER TABLE user_restrictions
ADD COLUMN prior_permissions_json TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE user_restrictions
DROP COLUMN prior_permissions_json;
