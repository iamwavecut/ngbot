-- +migrate Up
ALTER TABLE "chats" ADD COLUMN "gatekeeper_enabled" BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE "chats" ADD COLUMN "llm_first_message_enabled" BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE "chats" ADD COLUMN "community_voting_enabled" BOOLEAN NOT NULL DEFAULT 1;

UPDATE "chats" SET "gatekeeper_enabled" = "enabled";

CREATE TABLE IF NOT EXISTS "chat_managers" (
	"chat_id" INTEGER NOT NULL,
	"user_id" INTEGER NOT NULL,
	"can_manage_chat" BOOLEAN NOT NULL,
	"can_promote_members" BOOLEAN NOT NULL,
	"can_restrict_members" BOOLEAN NOT NULL,
	"updated_at" TIMESTAMP NOT NULL,
	PRIMARY KEY ("chat_id", "user_id"),
	FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "chat_bot_membership" (
	"chat_id" INTEGER PRIMARY KEY,
	"is_member" BOOLEAN NOT NULL,
	"updated_at" TIMESTAMP NOT NULL,
	FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "admin_panel_sessions" (
	"id" INTEGER PRIMARY KEY,
	"user_id" INTEGER NOT NULL,
	"chat_id" INTEGER NOT NULL,
	"page" TEXT NOT NULL,
	"state_json" TEXT NOT NULL,
	"message_id" INTEGER NOT NULL DEFAULT 0,
	"created_at" TIMESTAMP NOT NULL,
	"updated_at" TIMESTAMP NOT NULL,
	FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS "admin_panel_commands" (
	"id" INTEGER PRIMARY KEY,
	"session_id" INTEGER NOT NULL REFERENCES "admin_panel_sessions" ("id") ON DELETE CASCADE,
	"payload" TEXT NOT NULL,
	"created_at" TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS "chat_spam_examples" (
	"id" INTEGER PRIMARY KEY,
	"chat_id" INTEGER NOT NULL,
	"text" TEXT NOT NULL,
	"created_by_user_id" INTEGER NOT NULL,
	"created_at" TIMESTAMP NOT NULL,
	FOREIGN KEY ("chat_id") REFERENCES "chats" ("id") ON DELETE CASCADE
);

-- +migrate Down
DROP TABLE IF EXISTS "chat_spam_examples";
DROP TABLE IF EXISTS "admin_panel_commands";
DROP TABLE IF EXISTS "admin_panel_sessions";
DROP TABLE IF EXISTS "chat_bot_membership";
DROP TABLE IF EXISTS "chat_managers";
