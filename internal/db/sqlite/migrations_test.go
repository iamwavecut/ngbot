package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	migrate "github.com/rubenv/sql-migrate"
)

func TestSQLiteEnforcesForeignKeysAndCascades(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var enabled int
	if err := client.db.GetContext(ctx, &enabled, "PRAGMA foreign_keys"); err != nil {
		t.Fatalf("read foreign key mode: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign keys disabled: got %d", enabled)
	}

	if _, err := client.db.ExecContext(ctx, `INSERT INTO chat_members (chat_id, user_id) VALUES (?, ?)`, 999, 1); err == nil {
		t.Fatal("expected orphan chat member insert to fail")
	}

	settings := db.DefaultSettings(100)
	if err := client.SetSettings(ctx, settings); err != nil {
		t.Fatalf("create parent chat: %v", err)
	}
	if err := client.InsertMember(ctx, settings.ID, 1); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, settings.ID); err != nil {
		t.Fatalf("delete parent chat: %v", err)
	}

	var members int
	if err := client.db.GetContext(ctx, &members, `SELECT COUNT(*) FROM chat_members WHERE chat_id = ?`, settings.ID); err != nil {
		t.Fatalf("count cascaded members: %v", err)
	}
	if members != 0 {
		t.Fatalf("expected cascade to delete members, got %d", members)
	}

	rows, err := client.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("expected foreign_key_check to be empty")
	}

	dirInfo, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("unexpected data dir mode: got %o want 700", got)
	}
	dbInfo, err := os.Stat(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("unexpected database mode: got %o want 600", got)
	}
}

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
	defer func() { _ = rows.Close() }()

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
		"idx_spam_cases_chat_user",
		"idx_spam_cases_status",
		"idx_spam_cases_due_resolution",
	}
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
	defer func() { _ = rows.Close() }()

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
	defer func() { _ = rows.Close() }()

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

func TestGatekeeperWebAppChallengeColumnsExistAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	rows, err := client.db.QueryContext(ctx, "PRAGMA table_info('gatekeeper_challenges')")
	if err != nil {
		t.Fatalf("query gatekeeper_challenges columns: %v", err)
	}
	defer func() { _ = rows.Close() }()

	columns := make(map[string]struct{})
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate column rows: %v", err)
	}

	for _, name := range []string{
		"web_app_token",
		"join_request_query_id",
		"captcha_prompt",
		"captcha_options_json",
		"challenge_id",
		"next_attempt_at",
		"attempt_count",
		"last_error",
	} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("expected gatekeeper_challenges.%s to exist", name)
		}
	}

	indexRows, err := client.db.QueryContext(ctx, "PRAGMA index_list('gatekeeper_challenges')")
	if err != nil {
		t.Fatalf("query gatekeeper_challenges indexes: %v", err)
	}
	defer func() { _ = indexRows.Close() }()

	foundIndexes := map[string]bool{}
	for indexRows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := indexRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		foundIndexes[name] = true
	}
	if err := indexRows.Err(); err != nil {
		t.Fatalf("iterate index rows: %v", err)
	}
	for _, name := range []string{
		"idx_gatekeeper_challenges_web_app_token",
		"idx_gatekeeper_challenges_challenge_id",
		"idx_gatekeeper_challenges_due",
	} {
		if !foundIndexes[name] {
			t.Fatalf("expected gatekeeper index %q to exist", name)
		}
	}
}

func TestSpamCasesReportColumnsExistAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	rows, err := client.db.QueryContext(ctx, "PRAGMA table_info('spam_cases')")
	if err != nil {
		t.Fatalf("query spam_cases columns: %v", err)
	}
	defer func() { _ = rows.Close() }()

	columns := make(map[string]struct{})
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate column rows: %v", err)
	}

	for _, name := range []string{"message_id", "pre_vote_restricted"} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("expected spam_cases.%s to exist", name)
		}
	}
}

func TestSpamCaseReportMessagesTableExistsAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var name string
	if err := client.db.QueryRowContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name = 'spam_case_report_messages'
	`).Scan(&name); err != nil {
		t.Fatalf("expected spam_case_report_messages table: %v", err)
	}
	if name != "spam_case_report_messages" {
		t.Fatalf("unexpected table name %q", name)
	}
}

func TestCommunityVotingMigrationEnablesExistingChats(t *testing.T) {
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
		Root:       migrationsRoot,
	}
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, "20260513000000-add-voteban-report-flow.sql")); err != nil {
		t.Fatalf("execute pre-report migrations: %v", err)
	}

	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO chats (id, community_voting_enabled)
		VALUES (100, 0), (101, 1)
	`); err != nil {
		t.Fatalf("insert voting values: %v", err)
	}

	if _, err := migrate.Exec(sqlDB, "sqlite3", source, migrate.Up); err != nil {
		t.Fatalf("execute report migration: %v", err)
	}

	rows, err := sqlDB.QueryContext(ctx, `
		SELECT community_voting_enabled
		FROM chats
		WHERE id IN (100, 101)
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query migrated values: %v", err)
	}
	defer func() { _ = rows.Close() }()

	got := make([]bool, 0, 2)
	for rows.Next() {
		var enabled bool
		if err := rows.Scan(&enabled); err != nil {
			t.Fatalf("scan migrated value: %v", err)
		}
		got = append(got, enabled)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated values: %v", err)
	}
	if !slices.Equal(got, []bool{true, true}) {
		t.Fatalf("expected migration to enable community voting for all chats, got %#v", got)
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
		Root:       migrationsRoot,
	}
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, "20260511000000-rename-reaction-profile-check-setting.sql")); err != nil {
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
	defer func() { _ = rows.Close() }()

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

func TestDurableStateMigrationsUpgradeExistingRowsAndPreserveLegacySpamQuery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       migrationsRoot,
	}
	if _, err := migrate.ExecMax(
		sqlDB,
		"sqlite3",
		source,
		migrate.Up,
		migrationsBefore(t, "20260714000000-clean-orphans-before-foreign-keys.sql"),
	); err != nil {
		t.Fatalf("execute pre-durable migrations: %v", err)
	}

	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO chats (id) VALUES (100)`); err != nil {
		t.Fatalf("insert existing chat: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO chat_members (chat_id, user_id)
		VALUES (100, 1), (999, 2)
	`); err != nil {
		t.Fatalf("insert existing members: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO spam_cases (
			id, chat_id, user_id, message_id, message_text, created_at,
			channel_username, channel_post_id, notification_message_id,
			pre_vote_restricted, status, resolved_at
		) VALUES (7, 100, 1, 55, 'legacy message', CURRENT_TIMESTAMP,
			'legacy-channel', 8, 9, 1, 'pending', NULL)
	`); err != nil {
		t.Fatalf("insert existing spam case: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO gatekeeper_challenges (
			comm_chat_id, user_id, chat_id, success_uuid,
			created_at, expires_at, web_app_token
		) VALUES (100, 1, 200, 'success', CURRENT_TIMESTAMP, datetime('now', '+10 minutes'), 'token')
	`); err != nil {
		t.Fatalf("insert existing challenge: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO banlist (user_id) VALUES (1)`); err != nil {
		t.Fatalf("insert existing banlist entry: %v", err)
	}

	if _, err := migrate.Exec(sqlDB, "sqlite3", source, migrate.Up); err != nil {
		t.Fatalf("execute durable migrations: %v", err)
	}

	var validMembers int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_members WHERE chat_id = 100`).Scan(&validMembers); err != nil {
		t.Fatalf("count valid members: %v", err)
	}
	if validMembers != 1 {
		t.Fatalf("valid members = %d, want 1", validMembers)
	}
	var orphanMembers int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_members WHERE chat_id = 999`).Scan(&orphanMembers); err != nil {
		t.Fatalf("count orphan members: %v", err)
	}
	if orphanMembers != 0 {
		t.Fatalf("orphan members = %d, want 0", orphanMembers)
	}

	var challengeID string
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT challenge_id
		FROM gatekeeper_challenges
		WHERE comm_chat_id = 100 AND user_id = 1 AND chat_id = 200
	`).Scan(&challengeID); err != nil {
		t.Fatalf("read migrated challenge: %v", err)
	}
	if challengeID == "" {
		t.Fatal("migrated challenge has an empty generation ID")
	}

	var (
		caseID                int64
		chatID                int64
		userID                int64
		messageID             int
		messageText           string
		createdAt             time.Time
		channelUsername       sql.NullString
		channelPostID         sql.NullInt64
		notificationMessageID sql.NullInt64
		preVoteRestricted     bool
		status                string
		resolvedAt            sql.NullTime
	)
	err = sqlDB.QueryRowContext(ctx, `
		SELECT id, chat_id, user_id, message_id, message_text, created_at,
			channel_username, channel_post_id, notification_message_id,
			pre_vote_restricted, status, resolved_at
		FROM spam_cases
		WHERE id = 7
	`).Scan(
		&caseID,
		&chatID,
		&userID,
		&messageID,
		&messageText,
		&createdAt,
		&channelUsername,
		&channelPostID,
		&notificationMessageID,
		&preVoteRestricted,
		&status,
		&resolvedAt,
	)
	if err != nil {
		t.Fatalf("run legacy spam case query after upgrade: %v", err)
	}
	if caseID != 7 || chatID != 100 || userID != 1 || messageID != 55 || messageText != "legacy message" || status != db.SpamCaseStatusPending {
		t.Fatalf("legacy spam case values changed after upgrade")
	}

	var sourceRows int
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM banlist_sources
		WHERE user_id = 1 AND provider = 'legacy' AND feed_type = 'legacy'
	`).Scan(&sourceRows); err != nil {
		t.Fatalf("count migrated banlist source: %v", err)
	}
	if sourceRows != 1 {
		t.Fatalf("migrated banlist sources = %d, want 1", sourceRows)
	}

	rows, err := sqlDB.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("expected foreign_key_check to be empty after upgrade")
	}
}

func migrationsBefore(t *testing.T, target string) int {
	t.Helper()

	entries, err := resources.FS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if entry.Name() == target {
			return count
		}
		count++
	}
	t.Fatalf("migration %q not found", target)
	return 0
}
