package sqlite

import (
	"context"
	"testing"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestSpamCasesIndexesExistAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	rows, err := client.db.QueryContext(ctx, "PRAGMA index_list('spam_cases')")
	if err != nil {
		t.Fatalf("query index_list: %v", err)
	}
	defer rows.Close()

	indexes := make(map[string]struct{})
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		indexes[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index rows: %v", err)
	}

	required := []string{"idx_spam_cases_chat_user", "idx_spam_cases_status"}
	for _, name := range required {
		if _, ok := indexes[name]; !ok {
			t.Fatalf("required index %q not found", name)
		}
	}
}

func TestChatNotSpammerOverrideIndexesExistAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	rows, err := client.db.QueryContext(ctx, "PRAGMA index_list('chat_not_spammer_overrides')")
	if err != nil {
		t.Fatalf("query index_list: %v", err)
	}
	defer rows.Close()

	indexes := make(map[string]struct{})
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		indexes[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index rows: %v", err)
	}

	required := []string{
		"idx_chat_not_spammer_overrides_scope_match",
		"idx_chat_not_spammer_overrides_lookup",
	}
	for _, name := range required {
		if _, ok := indexes[name]; !ok {
			t.Fatalf("required index %q not found", name)
		}
	}
}

func TestChatKnownNonMemberPrimaryKeyIndexExistsAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	rows, err := client.db.QueryContext(ctx, "PRAGMA index_list('chat_known_non_members')")
	if err != nil {
		t.Fatalf("query index_list: %v", err)
	}
	defer rows.Close()

	foundPrimaryKeyIndex := false
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		if origin == "pk" {
			foundPrimaryKeyIndex = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index rows: %v", err)
	}
	if !foundPrimaryKeyIndex {
		t.Fatal("expected chat_known_non_members primary key index to exist")
	}
}

func TestReactionModerationSettingDefaultsToEnabledAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	settings := db.DefaultSettings(42)
	if err := client.SetSettings(ctx, settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	got, err := client.GetSettings(ctx, 42)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if !got.ReactionModerationEnabled {
		t.Fatal("expected reaction moderation to default to enabled")
	}
}
