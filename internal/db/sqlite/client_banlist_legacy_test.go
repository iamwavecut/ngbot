package sqlite

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	migrate "github.com/rubenv/sql-migrate"
)

func TestConvertLegacyBanlistCreatesSnapshotsOnce(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := t.TempDir()
	sqlDB, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: resources.FS, Root: migrationsRoot}
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, "20260715190000-cutover-banlist-generations.sql")+1); err != nil {
		t.Fatalf("apply prior migrations: %v", err)
	}
	casSeenAt := time.Date(2026, 10, 2, 6, 0, 0, 0, time.UTC)
	lolsSeenAt := time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO banlist_generations (id, provider, feed_type, generation, last_seen_at, expires_at, active)
		VALUES
			(1, 'cas', 'daily', 'cas-old', ?, NULL, 0),
			(2, 'cas', 'daily', 'cas-current', ?, NULL, 1),
			(3, 'lols', 'daily', 'lols-current', ?, NULL, 1);
		INSERT INTO banlist_entries (generation_id, user_id) VALUES (1, 100), (2, 9), (2, -3), (2, 7), (3, 1), (3, 2)
	`, casSeenAt.Add(-24*time.Hour), casSeenAt, lolsSeenAt); err != nil {
		t.Fatalf("seed legacy banlist: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	client, err := NewSQLiteClient(ctx, dir, "test.db")
	if err != nil {
		t.Fatalf("open converted client: %v", err)
	}
	converted := banlistSnapshotsBySource(t, client)
	if err := client.Close(); err != nil {
		t.Fatalf("close converted client: %v", err)
	}
	if len(converted) != 2 {
		t.Fatalf("converted %d snapshots, want 2: %+v", len(converted), converted)
	}
	cas := converted[db.BanlistSourceCASLegacy]
	if ids := decodedSnapshotIDs(t, cas); !slices.Equal(ids, []int64{-3, 7, 9}) || !cas.Frozen || !cas.CheckedAt.Equal(casSeenAt) {
		t.Fatalf("cas/legacy = ids %v frozen %v checked %v", ids, cas.Frozen, cas.CheckedAt)
	}
	lols := converted[db.BanlistSourceLoLsBanlist]
	if ids := decodedSnapshotIDs(t, lols); !slices.Equal(ids, []int64{1, 2}) || lols.Frozen || !lols.CheckedAt.Equal(time.Unix(0, 0)) {
		t.Fatalf("lols/banlist = ids %v frozen %v checked %v", ids, lols.Frozen, lols.CheckedAt)
	}

	reopened, err := NewSQLiteClient(ctx, dir, "test.db")
	if err != nil {
		t.Fatalf("reopen client: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for source, snapshot := range banlistSnapshotsBySource(t, reopened) {
		if !snapshot.ChangedAt.Equal(converted[source].ChangedAt) {
			t.Fatalf("%s changed_at moved from %v to %v on reopen", source, converted[source].ChangedAt, snapshot.ChangedAt)
		}
	}
}

func TestConvertLegacyBanlistWithoutLegacyTablesIsNoop(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `DROP TABLE banlist_entries; DROP TABLE banlist_generations; DROP TABLE banlist`); err != nil {
		t.Fatalf("drop legacy tables: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}

	reopened, err := NewSQLiteClient(ctx, dir, "test.db")
	if err != nil {
		t.Fatalf("reopen without legacy tables: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if snapshots := banlistSnapshotsBySource(t, reopened); len(snapshots) != 0 {
		t.Fatalf("snapshots without legacy tables = %+v, want none", snapshots)
	}
}
