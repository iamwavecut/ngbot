package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func newTestSQLiteClient(t *testing.T) *sqliteClient {
	t.Helper()

	client, err := NewSQLiteClient(context.Background(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestChatNotSpammerOverrideCreateListCountDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestSQLiteClient(t)

	createdAt := time.Now()
	override, err := client.CreateChatNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
		ChatID:          100,
		MatchType:       db.NotSpammerMatchTypeUsername,
		MatchValue:      "@Some_User",
		CreatedByUserID: 10,
		CreatedAt:       createdAt,
	})
	if err != nil {
		t.Fatalf("CreateChatNotSpammerOverride: %v", err)
	}

	duplicate, err := client.CreateChatNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
		ChatID:          100,
		MatchType:       db.NotSpammerMatchTypeUsername,
		MatchValue:      "some_user",
		CreatedByUserID: 10,
		CreatedAt:       createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("CreateChatNotSpammerOverride duplicate: %v", err)
	}
	if duplicate.ID != override.ID {
		t.Fatalf("expected duplicate create to return existing id %d, got %d", override.ID, duplicate.ID)
	}

	count, err := client.CountChatNotSpammerOverrides(ctx, 100)
	if err != nil {
		t.Fatalf("CountChatNotSpammerOverrides: %v", err)
	}
	if count != 1 {
		t.Fatalf("unexpected count: got %d want 1", count)
	}

	list, err := client.ListChatNotSpammerOverrides(ctx, 100, 10, 0)
	if err != nil {
		t.Fatalf("ListChatNotSpammerOverrides: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("unexpected list length: got %d want 1", len(list))
	}
	if list[0].MatchValue != "some_user" {
		t.Fatalf("unexpected canonical match value: %q", list[0].MatchValue)
	}

	got, err := client.GetChatNotSpammerOverride(ctx, 100, override.ID)
	if err != nil {
		t.Fatalf("GetChatNotSpammerOverride: %v", err)
	}
	if got == nil || got.ID != override.ID {
		t.Fatalf("unexpected override: %#v", got)
	}

	matched, err := client.IsChatNotSpammer(ctx, 100, 0, "Some_User")
	if err != nil {
		t.Fatalf("IsChatNotSpammer: %v", err)
	}
	if !matched {
		t.Fatal("expected username override to match")
	}

	matched, err = client.IsChatNotSpammer(ctx, 200, 0, "Some_User")
	if err != nil {
		t.Fatalf("IsChatNotSpammer other chat: %v", err)
	}
	if matched {
		t.Fatal("expected override not to match other chat")
	}

	if err := client.DeleteChatNotSpammerOverride(ctx, 100, override.ID); err != nil {
		t.Fatalf("DeleteChatNotSpammerOverride: %v", err)
	}

	count, err = client.CountChatNotSpammerOverrides(ctx, 100)
	if err != nil {
		t.Fatalf("CountChatNotSpammerOverrides after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("unexpected count after delete: got %d want 0", count)
	}
}

func TestChatNotSpammerOverrideCreateAllowsNegativeTelegramChatID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestSQLiteClient(t)

	override, err := client.CreateChatNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
		ChatID:          -1001234567890,
		MatchType:       db.NotSpammerMatchTypeUserID,
		MatchValue:      "12345",
		CreatedByUserID: 10,
		CreatedAt:       time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateChatNotSpammerOverride: %v", err)
	}
	if override == nil || override.ChatID != -1001234567890 {
		t.Fatalf("unexpected override: %#v", override)
	}

	matched, err := client.IsChatNotSpammer(ctx, -1001234567890, 12345, "")
	if err != nil {
		t.Fatalf("IsChatNotSpammer: %v", err)
	}
	if !matched {
		t.Fatal("expected negative Telegram chat id override to match")
	}
}

func TestChatNotSpammerOverrideGlobalLookupSupportsNullAndZero(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestSQLiteClient(t)

	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_not_spammer_overrides (chat_id, match_type, match_value, created_by_user_id, created_at)
		VALUES (NULL, ?, ?, ?, ?)
	`, db.NotSpammerMatchTypeUsername, "global_user", 1, time.Now()); err != nil {
		t.Fatalf("insert null-scope override: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_not_spammer_overrides (chat_id, match_type, match_value, created_by_user_id, created_at)
		VALUES (0, ?, ?, ?, ?)
	`, db.NotSpammerMatchTypeUserID, "12345", 1, time.Now()); err != nil {
		t.Fatalf("insert zero-scope override: %v", err)
	}

	usernameMatched, err := client.IsChatNotSpammer(ctx, 100, 0, "GLOBAL_USER")
	if err != nil {
		t.Fatalf("IsChatNotSpammer username global: %v", err)
	}
	if !usernameMatched {
		t.Fatal("expected NULL-scope override to match globally")
	}

	userIDMatched, err := client.IsChatNotSpammer(ctx, 200, 12345, "")
	if err != nil {
		t.Fatalf("IsChatNotSpammer user id global: %v", err)
	}
	if !userIDMatched {
		t.Fatal("expected zero-scope override to match globally")
	}
}

func TestChatNotSpammerOverrideAdminQueriesExcludeGlobalRowsAndEnforceNormalizedUniqueness(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestSQLiteClient(t)

	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_not_spammer_overrides (chat_id, match_type, match_value, created_by_user_id, created_at)
		VALUES (NULL, ?, ?, ?, ?)
	`, db.NotSpammerMatchTypeUsername, "duplicate_user", 1, time.Now()); err != nil {
		t.Fatalf("insert initial global override: %v", err)
	}

	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_not_spammer_overrides (chat_id, match_type, match_value, created_by_user_id, created_at)
		VALUES (0, ?, ?, ?, ?)
	`, db.NotSpammerMatchTypeUsername, "duplicate_user", 1, time.Now()); err == nil {
		t.Fatal("expected normalized-scope uniqueness violation for duplicate global override")
	}

	if _, err := client.CreateChatNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
		ChatID:          100,
		MatchType:       db.NotSpammerMatchTypeUsername,
		MatchValue:      "duplicate_user",
		CreatedByUserID: 1,
		CreatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("CreateChatNotSpammerOverride chat-scoped: %v", err)
	}

	count, err := client.CountChatNotSpammerOverrides(ctx, 100)
	if err != nil {
		t.Fatalf("CountChatNotSpammerOverrides: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected admin count to exclude global rows, got %d", count)
	}

	list, err := client.ListChatNotSpammerOverrides(ctx, 100, 10, 0)
	if err != nil {
		t.Fatalf("ListChatNotSpammerOverrides: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected admin list to exclude global rows, got %d items", len(list))
	}
}
