package sqlite

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"
)

func TestAuthorTrustMigrationPreservesEffectiveTrustAndExcludesUntrustedAuthors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: resources.FS, Root: migrationsRoot}
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, "20260818000000-add-context-aware-moderation.sql")+1); err != nil {
		t.Fatalf("apply prior migrations: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO chats (id) VALUES (-100), (-200);
		INSERT INTO chat_members (chat_id, user_id)
		VALUES (-100, 1), (-100, 2), (-100, 4), (-100, 5), (-100, 6), (-100, 9), (-100, -10), (-200, 4);
		INSERT INTO chat_message_probations (chat_id, user_id, started_at, eligible_at, graduated_at)
		VALUES
			(-100, 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, NULL),
			(-100, 3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			(-100, 5, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO chat_known_non_members (chat_id, user_id, created_at, updated_at)
		VALUES (-100, 7, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id)
		VALUES (-100, 40, 4);
		INSERT INTO spam_cases (id, chat_id, user_id, message_id, message_text, created_at, status)
		VALUES
			(4, -100, 4, 40, 'pending', CURRENT_TIMESTAMP, 'pending'),
			(5, -100, 5, 50, 'resolving', CURRENT_TIMESTAMP, 'resolving_spam'),
			(6, -100, 6, 60, 'resolving', CURRENT_TIMESTAMP, 'resolving_false_positive'),
			(9, -100, 9, 90, 'resolved', CURRENT_TIMESTAMP, 'false_positive');
		INSERT INTO spam_votes (case_id, voter_id, vote, voted_at)
		VALUES (4, 1000, TRUE, CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed legacy moderation: %v", err)
	}
	before := time.Now().UTC()
	if _, err := migrate.Exec(sqlDB, "sqlite3", source, migrate.Up); err != nil {
		t.Fatalf("apply author migration: %v", err)
	}
	var exists bool
	if err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'chat_author_trust')`).Scan(&exists); err != nil {
		t.Fatalf("inspect migrated schema: %v", err)
	}
	if !exists {
		t.Fatal("author trust migration did not create persistent trust")
	}
	rows, err := sqlDB.QueryContext(ctx, `SELECT chat_id, author_id, safe_messages, trusted_until FROM chat_author_trust WHERE trusted_until IS NOT NULL ORDER BY chat_id DESC, author_id`)
	if err != nil {
		t.Fatalf("read migrated trust: %v", err)
	}
	var authors [][2]int64
	for rows.Next() {
		var chatID, authorID int64
		var safeMessages int
		var trustedUntil time.Time
		if err := rows.Scan(&chatID, &authorID, &safeMessages, &trustedUntil); err != nil {
			t.Fatalf("scan migrated trust: %v", err)
		}
		if safeMessages != 3 || trustedUntil.Before(before.Add(30*24*time.Hour-time.Second)) || trustedUntil.After(time.Now().UTC().Add(30*24*time.Hour)) {
			t.Fatalf("incorrect migrated trust for %d/%d: safe=%d until=%v", chatID, authorID, safeMessages, trustedUntil)
		}
		authors = append(authors, [2]int64{chatID, authorID})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate trust: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close trust rows: %v", err)
	}
	if !slices.Equal(authors, [][2]int64{{-100, 1}, {-100, 3}, {-100, 4}, {-100, 5}, {-100, 6}, {-100, 9}, {-200, 4}}) {
		t.Fatalf("migrated trusted authors = %v", authors)
	}
	client := &sqliteClient{db: sqlx.NewDb(sqlDB, "sqlite")}
	for _, authorID := range []int64{4, 5, 6} {
		author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: authorID}
		trust, err := client.MessageTrust(ctx, -100, author)
		if err != nil || trust == nil || !trust.Suspended || trust.Trusted(before) {
			t.Fatalf("migrated active case bypassed suspension: trust=%+v err=%v", trust, err)
		}
		expiry := trust.TrustedUntil
		if _, err := sqlDB.ExecContext(ctx, `UPDATE spam_cases SET status = 'false_positive' WHERE id = ?`, authorID); err != nil {
			t.Fatalf("resolve migrated false positive: %v", err)
		}
		trust, err = client.MessageTrust(ctx, -100, author)
		if err != nil || trust == nil || !trust.Trusted(before) || trust.TrustedUntil != expiry {
			t.Fatalf("false positive lost migrated trust: trust=%+v err=%v", trust, err)
		}
	}
	for _, table := range []string{testTableSpamCases, testTableChallengedMessages} {
		var invalidKinds int
		if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE author_kind != 'user'`).Scan(&invalidKinds); err != nil {
			t.Fatalf("read legacy %s kinds: %v", table, err)
		}
		if invalidKinds != 0 {
			t.Fatalf("legacy %s kinds changed", table)
		}
	}
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Down, 1); err != nil {
		t.Fatalf("roll back author migration: %v", err)
	}
	for _, check := range []struct {
		table string
		want  int
	}{
		{table: testTableSpamCases, want: 4},
		{table: "spam_votes", want: 1},
		{table: testTableChallengedMessages, want: 1},
		{table: "chat_message_probations", want: 3},
	} {
		var count int
		if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+check.table).Scan(&count); err != nil || count != check.want {
			t.Fatalf("rollback %s count = %d, want %d: %v", check.table, count, check.want, err)
		}
	}
}

func TestAuthorTrustMigrationRejectsLossyRollback(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	if _, err := client.db.ExecContext(t.Context(), `
		INSERT INTO spam_cases (chat_id, author_kind, user_id, message_id, message_text, created_at, status)
		VALUES (-100, 'sender_chat', -300, 10, 'channel', CURRENT_TIMESTAMP, 'pending');
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, author_kind)
		VALUES (-100, 10, -300, 'sender_chat')
	`); err != nil {
		t.Fatalf("seed sender chat moderation: %v", err)
	}
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: resources.FS, Root: migrationsRoot}
	if _, err := migrate.ExecMax(client.db.DB, "sqlite3", source, migrate.Down, 1); err == nil {
		t.Fatal("rollback would reinterpret sender chat moderation as user moderation")
	}
	for _, table := range []string{testTableSpamCases, testTableChallengedMessages} {
		var kind string
		if err := client.db.GetContext(t.Context(), &kind, `SELECT author_kind FROM `+table); err != nil || kind != "sender_chat" {
			t.Fatalf("rejected rollback changed %s identity=%q err=%v", table, kind, err)
		}
	}
}
