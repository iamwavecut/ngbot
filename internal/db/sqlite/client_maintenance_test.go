package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMaintainDatabaseCompactsAndRestoresRuntimePragmas(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		CREATE TABLE maintenance_payload (id INTEGER PRIMARY KEY, payload BLOB NOT NULL);
		WITH RECURSIVE sequence(value) AS (
			SELECT 1
			UNION ALL
			SELECT value + 1 FROM sequence WHERE value < 512
		)
		INSERT INTO maintenance_payload (payload)
		SELECT randomblob(8192) FROM sequence;
		DELETE FROM maintenance_payload;
	`); err != nil {
		_ = client.Close()
		t.Fatalf("create reclaimable payload: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close sqlite client: %v", err)
	}

	dbPath := filepath.Join(dataDir, "test.db")
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat database before maintenance: %v", err)
	}
	if err := MaintainDatabase(ctx, dataDir, "test.db"); err != nil {
		t.Fatalf("maintain database: %v", err)
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat database after maintenance: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("maintenance did not compact database: before=%d after=%d", before.Size(), after.Size())
	}

	reopened, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen maintained database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var autoVacuum int
	if err := reopened.db.GetContext(ctx, &autoVacuum, `PRAGMA auto_vacuum`); err != nil {
		t.Fatalf("read maintained auto-vacuum mode: %v", err)
	}
	if autoVacuum != incrementalAutoVacuum {
		t.Fatalf("maintained auto-vacuum mode = %d, want incremental", autoVacuum)
	}
	var journalMode string
	if err := reopened.db.GetContext(ctx, &journalMode, `PRAGMA journal_mode`); err != nil {
		t.Fatalf("read maintained journal mode: %v", err)
	}
	if journalMode != walJournalMode {
		t.Fatalf("maintained journal mode = %q, want wal", journalMode)
	}
}
