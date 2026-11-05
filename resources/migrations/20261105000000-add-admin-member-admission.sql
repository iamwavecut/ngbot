-- +migrate Up
ALTER TABLE chat_author_trust ADD COLUMN admin_admitted_at TIMESTAMP;

-- +migrate Down
ALTER TABLE chat_author_trust DROP COLUMN admin_admitted_at;
