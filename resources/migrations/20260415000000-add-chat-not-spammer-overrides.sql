-- +migrate Up
CREATE TABLE IF NOT EXISTS "chat_not_spammer_overrides" (
	"id" INTEGER PRIMARY KEY,
	"chat_id" INTEGER NULL,
	"match_type" TEXT NOT NULL,
	"match_value" TEXT NOT NULL,
	"created_by_user_id" INTEGER NOT NULL,
	"created_at" TIMESTAMP NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS "idx_chat_not_spammer_overrides_scope_match"
ON "chat_not_spammer_overrides" (
	"match_type",
	"match_value",
	COALESCE(NULLIF(CAST("chat_id" AS TEXT), '0'), '')
);

CREATE INDEX IF NOT EXISTS "idx_chat_not_spammer_overrides_lookup"
ON "chat_not_spammer_overrides" (
	"match_type",
	"match_value",
	COALESCE(NULLIF(CAST("chat_id" AS TEXT), '0'), ''),
	"id" DESC
);

-- +migrate Down
DROP INDEX IF EXISTS "idx_chat_not_spammer_overrides_lookup";
DROP INDEX IF EXISTS "idx_chat_not_spammer_overrides_scope_match";
DROP TABLE IF EXISTS "chat_not_spammer_overrides";
