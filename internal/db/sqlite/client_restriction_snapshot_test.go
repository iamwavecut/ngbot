package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	migrate "github.com/rubenv/sql-migrate"
)

func TestRestrictionSnapshotPreservesPriorStatusAcrossReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client, err := NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	original := &db.UserRestriction{ChatID: -100, UserID: 200, RestrictedAt: now, ExpiresAt: now.Add(time.Hour), Reason: "Spam suspect", PriorMemberStatus: "member", PriorPermissionsJSON: `{"can_send_messages":true}`}
	if _, err := client.EnsureRestrictionSnapshot(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	replacement := *original
	replacement.PriorMemberStatus = "restricted"
	replacement.PriorPermissionsJSON = testNoMessagePermissionsJSON
	if _, err := client.EnsureRestrictionSnapshot(t.Context(), &replacement); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	got, err := client.GetRestriction(t.Context(), -100, 200)
	if err != nil || got == nil || got.PriorMemberStatus != "member" || got.PriorPermissionsJSON != original.PriorPermissionsJSON {
		t.Fatalf("snapshot changed across capture/reopen: got=%+v err=%v", got, err)
	}
	replacement.PriorUntilDate = now.Add(30 * time.Minute).Unix()
	if err := client.AddRestriction(t.Context(), &replacement); err != nil {
		t.Fatal(err)
	}
	got, err = client.GetRestriction(t.Context(), -100, 200)
	if err != nil || got == nil || got.PriorMemberStatus != "restricted" || got.PriorUntilDate != replacement.PriorUntilDate {
		t.Fatalf("restriction update lost prior status: got=%+v err=%v", got, err)
	}
}

func TestPriorMemberStatusMigrationPreservesLegacySnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: resources.FS, Root: migrationsRoot}
	migrations, err := source.FindMigrations()
	if err != nil {
		t.Fatal(err)
	}
	previous := make([]*migrate.Migration, 0, len(migrations))
	for _, migration := range migrations {
		if migration.Id != "20261104000000-add-prior-member-status.sql" {
			previous = append(previous, migration)
		}
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Exec(conn, "sqlite3", &migrate.MemoryMigrationSource{Migrations: previous}, migrate.Up); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := conn.Exec(`INSERT INTO user_restrictions(user_id,chat_id,restricted_at,expires_at,reason,prior_permissions_json,prior_until_date) VALUES(200,-100,?,?,'Spam suspect','{"can_send_messages":false}',?)`, now, now.Add(time.Hour), now.Add(5*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	client, err := NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	got, err := client.GetRestriction(t.Context(), -100, 200)
	if err != nil || got == nil || got.PriorMemberStatus != "" || got.PriorPermissionsJSON != testNoMessagePermissionsJSON || got.PriorUntilDate != now.Add(5*time.Minute).Unix() {
		t.Fatalf("migration changed legacy snapshot: got=%+v err=%v", got, err)
	}
}
