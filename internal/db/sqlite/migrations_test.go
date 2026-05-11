package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	migrate "github.com/rubenv/sql-migrate"
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

func TestReactionProfileCheckSettingDefaultsToEnabledAfterMigrations(t *testing.T) {
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
	if !got.ReactionProfileCheckEnabled {
		t.Fatal("expected reaction profile check to default to enabled")
	}
}

func TestReactionProfileCheckMigrationPreservesReactionModerationValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       "migrations",
	}
	migrationCount := countSQLMigrations(t)
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationCount-1); err != nil {
		t.Fatalf("execute pre-rename migrations: %v", err)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO chats (id, reaction_moderation_enabled)
		VALUES (100, 0), (101, 1)
	`); err != nil {
		t.Fatalf("insert old reaction moderation values: %v", err)
	}

	if _, err := migrate.Exec(sqlDB, "sqlite3", source, migrate.Up); err != nil {
		t.Fatalf("execute rename migration: %v", err)
	}

	rows, err := sqlDB.QueryContext(ctx, `
		SELECT id, reaction_profile_check_enabled
		FROM chats
		WHERE id IN (100, 101)
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query migrated values: %v", err)
	}
	defer rows.Close()

	got := map[int64]bool{}
	for rows.Next() {
		var id int64
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			t.Fatalf("scan migrated value: %v", err)
		}
		got[id] = enabled
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated values: %v", err)
	}
	if got[100] {
		t.Fatalf("expected disabled old value to stay disabled, got %#v", got)
	}
	if !got[101] {
		t.Fatalf("expected enabled old value to stay enabled, got %#v", got)
	}
}

func countSQLMigrations(t *testing.T) int {
	t.Helper()

	entries, err := resources.FS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			count++
		}
	}
	if count == 0 {
		t.Fatal("expected at least one migration")
	}
	return count
}
