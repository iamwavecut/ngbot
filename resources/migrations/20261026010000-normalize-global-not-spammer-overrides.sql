-- +migrate Up
UPDATE "chat_not_spammer_overrides" SET "chat_id" = 0 WHERE "chat_id" IS NULL;

-- +migrate Down
