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
	"github.com/jmoiron/sqlx"
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
	var busyTimeout int
	if err := client.db.GetContext(ctx, &busyTimeout, "PRAGMA busy_timeout"); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy timeout = %d, want 5000", busyTimeout)
	}
	var journalMode string
	if err := client.db.GetContext(ctx, &journalMode, "PRAGMA journal_mode"); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	var journalSizeLimit int64
	if err := client.db.GetContext(ctx, &journalSizeLimit, "PRAGMA journal_size_limit"); err != nil {
		t.Fatalf("read journal size limit: %v", err)
	}
	if journalSizeLimit != journalSizeLimitBytes {
		t.Fatalf("journal size limit = %d, want %d", journalSizeLimit, journalSizeLimitBytes)
	}
	var walAutoCheckpoint int
	if err := client.db.GetContext(ctx, &walAutoCheckpoint, "PRAGMA wal_autocheckpoint"); err != nil {
		t.Fatalf("read WAL auto-checkpoint: %v", err)
	}
	if walAutoCheckpoint != walAutoCheckpointPages {
		t.Fatalf("WAL auto-checkpoint = %d, want %d", walAutoCheckpoint, walAutoCheckpointPages)
	}
	var autoVacuum int
	if err := client.db.GetContext(ctx, &autoVacuum, "PRAGMA auto_vacuum"); err != nil {
		t.Fatalf("read auto-vacuum mode: %v", err)
	}
	if autoVacuum != incrementalAutoVacuum {
		t.Fatalf("auto-vacuum mode = %d, want incremental", autoVacuum)
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
		"notice_message_id",
		"user_restricted",
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

	var generationRows int
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM banlist_generations
	`).Scan(&generationRows); err != nil {
		t.Fatalf("count migrated banlist generations: %v", err)
	}
	if generationRows != 0 {
		t.Fatalf("legacy-only banlist generations survived cutover: %d", generationRows)
	}
	var effectiveRows int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM banlist`).Scan(&effectiveRows); err != nil {
		t.Fatalf("count effective banlist after cutover: %v", err)
	}
	if effectiveRows != 0 {
		t.Fatalf("legacy-only effective banlist rows survived cutover: %d", effectiveRows)
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

func TestBanlistGenerationMigrationRetiresLegacyAndPreservesActiveProviders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "banlist-cutover.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       migrationsRoot,
	}
	const migration = "20260715190000-cutover-banlist-generations.sql"
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, migration)); err != nil {
		t.Fatalf("execute migrations before banlist cutover: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		DELETE FROM banlist_sources;
		DELETE FROM banlist;
		INSERT INTO banlist (user_id) VALUES (1), (2), (3), (4);
		INSERT INTO banlist_sources (
			user_id, provider, feed_type, generation, last_seen_at, expires_at
		) VALUES
			(1, 'legacy', 'legacy', 'migration', CURRENT_TIMESTAMP, datetime('now', '+1 day')),
			(2, 'lols', 'daily', 'lols-current', CURRENT_TIMESTAMP, NULL),
			(3, 'cas', 'daily', 'cas-current', CURRENT_TIMESTAMP, NULL),
			(4, 'lols', 'hourly', 'lols-expired', datetime('now', '-2 days'), datetime('now', '-1 hour'))
	`); err != nil {
		t.Fatalf("seed pre-cutover banlist: %v", err)
	}

	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, 1); err != nil {
		t.Fatalf("execute banlist cutover migration: %v", err)
	}

	var oldTableRows int
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'banlist_sources'
	`).Scan(&oldTableRows); err != nil {
		t.Fatalf("check retired source table: %v", err)
	}
	if oldTableRows != 0 {
		t.Fatal("legacy banlist_sources table survived cutover")
	}
	var effective []int64
	if err := sqlx.NewDb(sqlDB, "sqlite").SelectContext(ctx, &effective, `SELECT user_id FROM banlist ORDER BY user_id`); err != nil {
		t.Fatalf("read cutover projection: %v", err)
	}
	if !slices.Equal(effective, []int64{2, 3}) {
		t.Fatalf("cutover projection = %v, want [2 3]", effective)
	}
	var generationRows int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM banlist_generations WHERE active = 1`).Scan(&generationRows); err != nil {
		t.Fatalf("count active generations: %v", err)
	}
	if generationRows != 2 {
		t.Fatalf("active generations = %d, want 2", generationRows)
	}
	var entryRows int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM banlist_entries`).Scan(&entryRows); err != nil {
		t.Fatalf("count generation entries: %v", err)
	}
	if entryRows != 2 {
		t.Fatalf("generation entries = %d, want 2", entryRows)
	}

	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Down, 1); err != nil {
		t.Fatalf("roll back banlist cutover migration: %v", err)
	}
	var restoredSources int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM banlist_sources`).Scan(&restoredSources); err != nil {
		t.Fatalf("count restored banlist sources: %v", err)
	}
	if restoredSources != 2 {
		t.Fatalf("restored banlist sources = %d, want 2", restoredSources)
	}
}

func TestLegacySpamRecoveryMigrationIsScopedAndReversible(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy-spam.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       migrationsRoot,
	}
	const migration = "20260714210000-finalize-legacy-spam-cases.sql"
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, migration)); err != nil {
		t.Fatalf("execute migrations before legacy recovery: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO chats (id) VALUES (100)`); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO spam_cases (
			id, chat_id, user_id, message_id, message_text, created_at,
			pre_vote_restricted, status, resolve_at, next_attempt_at, attempt_count, last_error
		) VALUES
			(1, 100, 1, 0, 'legacy resolving', CURRENT_TIMESTAMP, 1,
				'resolving_false_positive', datetime('now', '-1 minute'), datetime('now', '+1 day'), 3, 'legacy error'),
			(2, 100, 2, 0, 'legacy pending', CURRENT_TIMESTAMP, 1,
				'pending', datetime('now', '+1 minute'), NULL, 0, ''),
			(3, 100, 3, 55, 'modern resolving', CURRENT_TIMESTAMP, 1,
				'resolving_false_positive', datetime('now', '-1 minute'), datetime('now', '+1 day'), 2, 'modern error')
	`); err != nil {
		t.Fatalf("insert spam cases: %v", err)
	}

	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, 1); err != nil {
		t.Fatalf("execute legacy recovery migration: %v", err)
	}

	for _, caseID := range []int64{1, 2} {
		var (
			status        string
			resolvedAt    sql.NullTime
			nextAttemptAt sql.NullTime
		)
		if err := sqlDB.QueryRowContext(ctx, `
			SELECT status, resolved_at, next_attempt_at
			FROM spam_cases
			WHERE id = ?
		`, caseID).Scan(&status, &resolvedAt, &nextAttemptAt); err != nil {
			t.Fatalf("read finalized legacy case %d: %v", caseID, err)
		}
		if status != db.SpamCaseStatusFalsePositive || !resolvedAt.Valid || nextAttemptAt.Valid {
			t.Fatalf("legacy case %d was not finalized: status=%q resolved=%v next=%v", caseID, status, resolvedAt.Valid, nextAttemptAt.Valid)
		}
	}
	var modernStatus string
	if err := sqlDB.QueryRowContext(ctx, `SELECT status FROM spam_cases WHERE id = 3`).Scan(&modernStatus); err != nil {
		t.Fatalf("read modern case: %v", err)
	}
	if modernStatus != db.SpamCaseStatusResolvingFalsePositive {
		t.Fatalf("modern case status = %q, want resolving_false_positive", modernStatus)
	}
	var backupRows int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_spam_legacy_recovery_backup`).Scan(&backupRows); err != nil {
		t.Fatalf("count backup rows: %v", err)
	}
	if backupRows != 2 {
		t.Fatalf("backup rows = %d, want 2", backupRows)
	}

	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Down, 1); err != nil {
		t.Fatalf("roll back legacy recovery migration: %v", err)
	}
	var (
		restoredStatus   string
		restoredAttempts int
		restoredError    string
		restoredNext     sql.NullTime
	)
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT status, attempt_count, last_error, next_attempt_at
		FROM spam_cases
		WHERE id = 1
	`).Scan(&restoredStatus, &restoredAttempts, &restoredError, &restoredNext); err != nil {
		t.Fatalf("read restored legacy case: %v", err)
	}
	if restoredStatus != db.SpamCaseStatusResolvingFalsePositive || restoredAttempts != 3 || restoredError != "legacy error" || !restoredNext.Valid {
		t.Fatalf("legacy case was not restored exactly: status=%q attempts=%d error=%q next=%v", restoredStatus, restoredAttempts, restoredError, restoredNext.Valid)
	}
}

func TestImmediateSpamRecoveryMigrationIsScopedAndReversible(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "immediate-spam.db")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       migrationsRoot,
	}
	const migration = "20260714220000-recover-immediate-spam-cases.sql"
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, migration)); err != nil {
		t.Fatalf("execute migrations before immediate recovery: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO chats (id) VALUES (100)`); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO spam_cases (
			id, chat_id, user_id, message_id, message_text, created_at,
			pre_vote_restricted, status, resolve_at, next_attempt_at, attempt_count, last_error
		) VALUES
			(1, 100, 1, 55, 'immediate pending', CURRENT_TIMESTAMP, 0,
				'pending', NULL, datetime('now', '+1 day'), 3, 'previous error'),
			(2, 100, 2, 56, 'voting pending', CURRENT_TIMESTAMP, 1,
				'pending', NULL, NULL, 0, ''),
			(3, 100, 3, 57, 'reported pending', CURRENT_TIMESTAMP, 0,
				'pending', datetime('now', '+1 minute'), NULL, 0, ''),
			(4, 100, 4, 0, 'legacy pending', CURRENT_TIMESTAMP, 0,
				'pending', NULL, NULL, 0, '')
	`); err != nil {
		t.Fatalf("insert spam cases: %v", err)
	}

	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, 1); err != nil {
		t.Fatalf("execute immediate recovery migration: %v", err)
	}

	var (
		status        string
		nextAttemptAt sql.NullTime
		attempts      int
		lastError     string
	)
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT status, next_attempt_at, attempt_count, last_error
		FROM spam_cases WHERE id = 1
	`).Scan(&status, &nextAttemptAt, &attempts, &lastError); err != nil {
		t.Fatalf("read recovered immediate case: %v", err)
	}
	if status != db.SpamCaseStatusResolvingSpam || !nextAttemptAt.Valid || attempts != 0 || lastError != "" {
		t.Fatalf("immediate case was not made durable: status=%q next=%v attempts=%d error=%q", status, nextAttemptAt.Valid, attempts, lastError)
	}
	for _, caseID := range []int64{2, 3, 4} {
		if err := sqlDB.QueryRowContext(ctx, `SELECT status FROM spam_cases WHERE id = ?`, caseID).Scan(&status); err != nil {
			t.Fatalf("read unaffected case %d: %v", caseID, err)
		}
		if status != db.SpamCaseStatusPending {
			t.Fatalf("unrelated case %d changed to %q", caseID, status)
		}
	}
	var backupRows int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_spam_immediate_recovery_backup`).Scan(&backupRows); err != nil {
		t.Fatalf("count immediate recovery backups: %v", err)
	}
	if backupRows != 1 {
		t.Fatalf("backup rows = %d, want 1", backupRows)
	}

	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Down, 1); err != nil {
		t.Fatalf("roll back immediate recovery migration: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT status, next_attempt_at, attempt_count, last_error
		FROM spam_cases WHERE id = 1
	`).Scan(&status, &nextAttemptAt, &attempts, &lastError); err != nil {
		t.Fatalf("read restored immediate case: %v", err)
	}
	if status != db.SpamCaseStatusPending || !nextAttemptAt.Valid || attempts != 3 || lastError != "previous error" {
		t.Fatalf("immediate case was not restored exactly: status=%q next=%v attempts=%d error=%q", status, nextAttemptAt.Valid, attempts, lastError)
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
