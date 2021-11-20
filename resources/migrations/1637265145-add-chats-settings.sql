-- +migrate Up
ALTER TABLE "chats" 
    ADD COLUMN "settings" TEXT;

-- +migrate Down
-- nothing to do
