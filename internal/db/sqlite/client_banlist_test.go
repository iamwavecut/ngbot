package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	migrate "github.com/rubenv/sql-migrate"
)

const testOnlineObservationSource = "lols/online"

func testBanlistSnapshot(t testing.TB, source string, ids []int64, at time.Time) *db.BanlistSnapshot {
	t.Helper()
	payload, err := db.EncodeBanlistIDs(ids)
	if err != nil {
		t.Fatalf("encode banlist IDs: %v", err)
	}
	sum := sha256.Sum256(payload)
	return &db.BanlistSnapshot{
		Source:        source,
		Format:        db.BanlistSnapshotFormatV1,
		IDCount:       len(ids),
		Payload:       payload,
		PayloadSHA256: sum[:],
		ETag:          sql.NullString{String: `"v1"`, Valid: true},
		LastModified:  sql.NullString{String: "Mon, 26 Oct 2026 08:00:00 GMT", Valid: true},
		ChangedAt:     at,
		CheckedAt:     at,
	}
}

func banlistSnapshotsBySource(t *testing.T, client *sqliteClient) map[string]db.BanlistSnapshot {
	t.Helper()
	snapshots, err := client.BanlistSnapshots(t.Context())
	if err != nil {
		t.Fatalf("load banlist snapshots: %v", err)
	}
	bySource := make(map[string]db.BanlistSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		bySource[snapshot.Source] = snapshot
	}
	return bySource
}

func decodedSnapshotIDs(t *testing.T, snapshot db.BanlistSnapshot) []int64 {
	t.Helper()
	ids, err := db.DecodeBanlistIDs(snapshot.Payload, snapshot.IDCount)
	if err != nil {
		t.Fatalf("decode %s: %v", snapshot.Source, err)
	}
	return ids
}

func TestBanlistSnapshotsMigrationCreatesTablesAndCopiesTTLRows(t *testing.T) {
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
	if _, err := migrate.ExecMax(sqlDB, "sqlite3", source, migrate.Up, migrationsBefore(t, "20260715190000-cutover-banlist-generations.sql")+1); err != nil {
		t.Fatalf("apply prior migrations: %v", err)
	}
	now := time.Now().UTC()
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO banlist_generations (id, provider, feed_type, generation, last_seen_at, expires_at, active)
		VALUES
			(1, 'lols', 'online', 'online-1', ?, ?, 1),
			(2, 'cas', 'online', 'online-2', ?, ?, 1),
			(3, 'lols', 'daily', 'daily-1', ?, NULL, 1);
		INSERT INTO banlist_entries (generation_id, user_id) VALUES (1, 11), (2, 22), (3, 33)
	`, now.Add(-time.Hour), now.Add(24*time.Hour), now.Add(-48*time.Hour), now.Add(-24*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed legacy banlist: %v", err)
	}
	if _, err := migrate.Exec(sqlDB, "sqlite3", source, migrate.Up); err != nil {
		t.Fatalf("apply snapshot migration: %v", err)
	}

	var snapshots int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM banlist_snapshots`).Scan(&snapshots); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshots != 0 {
		t.Fatalf("migration created %d snapshots, want 0", snapshots)
	}
	rows, err := sqlDB.QueryContext(ctx, `SELECT user_id, source, expires_at FROM banlist_observations ORDER BY user_id`)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []db.BanlistObservation
	for rows.Next() {
		var observation db.BanlistObservation
		if err := rows.Scan(&observation.UserID, &observation.Source, &observation.ExpiresAt); err != nil {
			t.Fatalf("scan observation: %v", err)
		}
		got = append(got, observation)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate observations: %v", err)
	}
	if len(got) != 1 || got[0].UserID != 11 || got[0].Source != testOnlineObservationSource || !got[0].ExpiresAt.After(now) {
		t.Fatalf("observations = %+v, want only unexpired user 11 from lols/online", got)
	}
}

func TestSaveAndLoadBanlistSnapshot(t *testing.T) {
	t.Parallel()

	client := newTestSQLiteClient(t)
	at := time.Date(2026, 10, 26, 8, 30, 0, 0, time.UTC)
	cases := map[string][]int64{
		db.BanlistSourceLoLsBanlist:  {-5, 3, 9},
		db.BanlistSourceLoLsScammers: {},
	}
	for source, ids := range cases {
		snapshot := testBanlistSnapshot(t, source, ids, at)
		snapshot.Frozen = source == db.BanlistSourceLoLsBanlist
		if err := client.SaveBanlistSnapshot(t.Context(), snapshot); err != nil {
			t.Fatalf("save %s: %v", source, err)
		}
	}

	stored := banlistSnapshotsBySource(t, client)
	if len(stored) != len(cases) {
		t.Fatalf("loaded %d snapshots, want %d", len(stored), len(cases))
	}
	for source, ids := range cases {
		want := testBanlistSnapshot(t, source, ids, at)
		got, ok := stored[source]
		if !ok {
			t.Fatalf("snapshot %s is missing", source)
		}
		if got.Format != want.Format || got.IDCount != want.IDCount || !bytes.Equal(got.Payload, want.Payload) ||
			!bytes.Equal(got.PayloadSHA256, want.PayloadSHA256) || got.ETag != want.ETag || got.LastModified != want.LastModified ||
			got.Frozen != (source == db.BanlistSourceLoLsBanlist) || !got.ChangedAt.Equal(at) || !got.CheckedAt.Equal(at) {
			t.Fatalf("snapshot %s = %+v, want %+v", source, got, want)
		}
		if decoded := decodedSnapshotIDs(t, got); !slices.Equal(decoded, ids) {
			t.Fatalf("snapshot %s IDs = %v, want %v", source, decoded, ids)
		}
	}
}

func TestTouchBanlistSnapshotUpdatesOnlyValidators(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client := newTestSQLiteClient(t)
	at := time.Date(2026, 10, 26, 8, 30, 0, 0, time.UTC)
	original := testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, []int64{1, 2, 3}, at)
	if err := client.SaveBanlistSnapshot(ctx, original); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	checkedAt := at.Add(time.Hour)
	touched, err := client.TouchBanlistSnapshot(ctx, db.BanlistSourceLoLsBanlist, sql.NullString{String: `"v2"`, Valid: true}, sql.NullString{}, checkedAt)
	if err != nil || !touched {
		t.Fatalf("touch snapshot = %v, %v; want true, nil", touched, err)
	}
	got := banlistSnapshotsBySource(t, client)[db.BanlistSourceLoLsBanlist]
	if got.ETag.String != `"v2"` || !got.ETag.Valid || got.LastModified.Valid || !got.CheckedAt.Equal(checkedAt) {
		t.Fatalf("validators = etag %+v last_modified %+v checked_at %v", got.ETag, got.LastModified, got.CheckedAt)
	}
	if !bytes.Equal(got.Payload, original.Payload) || !bytes.Equal(got.PayloadSHA256, original.PayloadSHA256) ||
		got.IDCount != original.IDCount || !got.ChangedAt.Equal(at) {
		t.Fatalf("touch changed payload metadata: %+v", got)
	}

	touched, err = client.TouchBanlistSnapshot(ctx, "missing/source", sql.NullString{}, sql.NullString{}, checkedAt)
	if err != nil || touched {
		t.Fatalf("touch missing snapshot = %v, %v; want false, nil", touched, err)
	}
}

func TestSaveBanlistSnapshotCanceledKeepsPreviousRow(t *testing.T) {
	t.Parallel()

	client := newTestSQLiteClient(t)
	at := time.Date(2026, 10, 26, 8, 30, 0, 0, time.UTC)
	versionA := testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, []int64{1, 2, 3}, at)
	if err := client.SaveBanlistSnapshot(t.Context(), versionA); err != nil {
		t.Fatalf("save version A: %v", err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	versionB := testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, []int64{4, 5}, at.Add(time.Hour))
	if err := client.SaveBanlistSnapshot(canceled, versionB); err == nil {
		t.Fatal("save with canceled context succeeded")
	}

	got := banlistSnapshotsBySource(t, client)[db.BanlistSourceLoLsBanlist]
	if !bytes.Equal(got.Payload, versionA.Payload) || got.IDCount != versionA.IDCount {
		t.Fatalf("stored snapshot = %+v, want version A", got)
	}
}

func TestBanlistObservationsExpiry(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client := newTestSQLiteClient(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := client.UpsertBanlistObservations(ctx, []db.BanlistObservation{
		{UserID: 1, Source: testOnlineObservationSource, ObservedAt: now.Add(-time.Hour), ExpiresAt: now.Add(23 * time.Hour)},
		{UserID: 2, Source: db.BanlistSourceLoLsHourly, ObservedAt: now.Add(-30 * time.Hour), ExpiresAt: now.Add(-4 * time.Hour)},
	}); err != nil {
		t.Fatalf("upsert observations: %v", err)
	}

	active, err := client.BanlistObservations(ctx, now)
	if err != nil {
		t.Fatalf("load observations: %v", err)
	}
	if len(active) != 1 || active[0].UserID != 1 || active[0].Source != testOnlineObservationSource {
		t.Fatalf("active observations = %+v, want only user 1", active)
	}

	if err := client.UpsertBanlistObservations(ctx, []db.BanlistObservation{
		{UserID: 1, Source: testOnlineObservationSource, ObservedAt: now, ExpiresAt: now.Add(time.Hour)},
	}); err != nil {
		t.Fatalf("upsert shorter observation: %v", err)
	}
	active, err = client.BanlistObservations(ctx, now)
	if err != nil {
		t.Fatalf("reload observations: %v", err)
	}
	if len(active) != 1 || !active[0].ObservedAt.Equal(now) || !active[0].ExpiresAt.Equal(now.Add(23*time.Hour)) {
		t.Fatalf("observation after shorter upsert = %+v, want observed now and unchanged expiry", active)
	}

	deleted, err := client.DeleteExpiredBanlistObservations(ctx, now, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("delete expired = %d, %v; want 1, nil", deleted, err)
	}
	remaining, err := client.BanlistObservations(ctx, now.Add(-10*time.Hour))
	if err != nil {
		t.Fatalf("load remaining observations: %v", err)
	}
	if len(remaining) != 1 || remaining[0].UserID != 1 {
		t.Fatalf("remaining observations = %+v, want only user 1", remaining)
	}
	deleted, err = client.DeleteExpiredBanlistObservations(ctx, now, 1)
	if err != nil || deleted != 0 {
		t.Fatalf("second delete expired = %d, %v; want 0, nil", deleted, err)
	}
}

func BenchmarkSaveBanlistSnapshot5M(b *testing.B) {
	ctx := b.Context()
	dir := b.TempDir()
	client, err := NewSQLiteClient(ctx, dir, "bot.db")
	if err != nil {
		b.Fatalf("new sqlite client: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })

	ids := make([]int64, 5_000_000)
	next := int64(100_000_000)
	for i := range ids {
		next += int64(1 + (i*7919)%3_500)
		ids[i] = next
	}
	snapshot := testBanlistSnapshot(b, db.BanlistSourceLoLsBanlist, ids, time.Now().UTC())
	if err := client.SaveBanlistSnapshot(ctx, snapshot); err != nil {
		b.Fatalf("initial save: %v", err)
	}
	databaseBytes := func() int64 {
		var busy, logFrames, checkpointedFrames int
		if err := client.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
			b.Fatalf("checkpoint WAL: %v", err)
		}
		var total int64
		for _, name := range []string{"bot.db", "bot.db-wal"} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err == nil {
				total += info.Size()
			}
		}
		return total
	}
	before := databaseBytes()
	saves := 0
	for b.Loop() {
		snapshot.ChangedAt = time.Now().UTC()
		if err := client.SaveBanlistSnapshot(ctx, snapshot); err != nil {
			b.Fatalf("save: %v", err)
		}
		saves++
	}
	b.ReportMetric(float64(databaseBytes()-before)/float64(saves), "growth-bytes/save")
	b.ReportMetric(float64(len(snapshot.Payload)), "payload-bytes")
}
