package sqlite

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestBanlistProviderSnapshotRemovesOnlyUnconfirmedIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC()
	added, removed, err := client.ApplyBanlistSource(ctx, "lols", "daily", "lols-1", []int64{1, 2}, now, nil, true)
	if err != nil {
		t.Fatalf("apply first lols snapshot: %v", err)
	}
	assertBanlistDelta(t, added, removed, []int64{1, 2}, nil)
	added, removed, err = client.ApplyBanlistSource(ctx, "cas", "daily", "cas-1", []int64{1, 3}, now, nil, true)
	if err != nil {
		t.Fatalf("apply cas snapshot: %v", err)
	}
	assertBanlistDelta(t, added, removed, []int64{3}, nil)
	added, removed, err = client.ApplyBanlistSource(ctx, "lols", "daily", "lols-2", []int64{2}, now.Add(time.Hour), nil, true)
	if err != nil {
		t.Fatalf("replace lols snapshot: %v", err)
	}
	assertBanlistDelta(t, added, removed, nil, nil)

	banlist, err := client.GetBanlist(ctx)
	if err != nil {
		t.Fatalf("get effective banlist: %v", err)
	}
	for _, userID := range []int64{1, 2, 3} {
		if _, ok := banlist[userID]; !ok {
			t.Fatalf("expected user %d to remain confirmed: %#v", userID, banlist)
		}
	}

	added, removed, err = client.ApplyBanlistSource(ctx, "cas", "daily", "cas-2", []int64{3}, now.Add(2*time.Hour), nil, true)
	if err != nil {
		t.Fatalf("replace cas snapshot: %v", err)
	}
	assertBanlistDelta(t, added, removed, nil, []int64{1})
	banlist, err = client.GetBanlist(ctx)
	if err != nil {
		t.Fatalf("get effective banlist after rehabilitation: %v", err)
	}
	if _, ok := banlist[1]; ok {
		t.Fatalf("expected user 1 to be removed after all providers dropped it: %#v", banlist)
	}
}

func TestBanlistExpiringSourcesArePrunedDuringRebuild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC()
	expiresAt := now.Add(26 * time.Hour)
	added, removed, err := client.ApplyBanlistSource(ctx, "lols", "hourly", "hour-1", []int64{4}, now, &expiresAt, false)
	if err != nil {
		t.Fatalf("apply hourly source: %v", err)
	}
	assertBanlistDelta(t, added, removed, []int64{4}, nil)
	added, removed, err = client.ApplyBanlistSource(ctx, "maintenance", "expiry", "tick", nil, now.Add(27*time.Hour), nil, false)
	if err != nil {
		t.Fatalf("rebuild after expiry: %v", err)
	}
	assertBanlistDelta(t, added, removed, nil, []int64{4})

	banlist, err := client.GetBanlist(ctx)
	if err != nil {
		t.Fatalf("get effective banlist: %v", err)
	}
	if _, ok := banlist[4]; ok {
		t.Fatalf("expected expired hourly source to be removed: %#v", banlist)
	}
}

func TestIncrementalBanlistUpdateDoesNotRewriteEffectiveProjection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC()
	if _, _, err := client.ApplyBanlistSource(ctx, "lols", "daily", "daily-1", []int64{1, 2, 3}, now, nil, true); err != nil {
		t.Fatalf("seed daily source: %v", err)
	}
	for _, query := range []string{
		`CREATE TABLE banlist_mutations (action TEXT NOT NULL, user_id INTEGER NOT NULL)`,
		`CREATE TRIGGER audit_banlist_insert AFTER INSERT ON banlist BEGIN INSERT INTO banlist_mutations VALUES ('insert', NEW.user_id); END`,
		`CREATE TRIGGER audit_banlist_delete AFTER DELETE ON banlist BEGIN INSERT INTO banlist_mutations VALUES ('delete', OLD.user_id); END`,
	} {
		if _, err := client.db.ExecContext(ctx, query); err != nil {
			t.Fatalf("create mutation audit: %v", err)
		}
	}

	expiresAt := now.Add(26 * time.Hour)
	if _, _, err := client.ApplyBanlistSource(ctx, "lols", "hourly", "hourly-1", []int64{2, 4}, now, &expiresAt, false); err != nil {
		t.Fatalf("apply incremental source: %v", err)
	}
	var mutations []string
	if err := client.db.SelectContext(ctx, &mutations, `SELECT action || ':' || user_id FROM banlist_mutations ORDER BY rowid`); err != nil {
		t.Fatalf("read projection mutations: %v", err)
	}
	if !slices.Equal(mutations, []string{"insert:4"}) {
		t.Fatalf("incremental update rewrote unaffected projection rows: %#v", mutations)
	}
}

func TestBanlistImportCanCommitWhileReadGuardIsHeld(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	client.mutex.RLock()
	done := make(chan error, 1)
	go func() {
		_, _, err := client.ApplyBanlistSource(ctx, "lols", "hourly", "hourly-1", []int64{1}, time.Now(), nil, false)
		done <- err
	}()

	select {
	case err := <-done:
		client.mutex.RUnlock()
		if err != nil {
			t.Fatalf("apply banlist source while read guard is held: %v", err)
		}
	case <-time.After(2 * time.Second):
		client.mutex.RUnlock()
		if err := <-done; err != nil {
			t.Fatalf("apply banlist source after releasing read guard: %v", err)
		}
		t.Fatal("banlist import waited for an unrelated database reader")
	}
}

func assertBanlistDelta(t *testing.T, added, removed, wantAdded, wantRemoved []int64) {
	t.Helper()
	slices.Sort(added)
	slices.Sort(removed)
	if !slices.Equal(added, wantAdded) || !slices.Equal(removed, wantRemoved) {
		t.Fatalf("banlist delta added=%v removed=%v, want added=%v removed=%v", added, removed, wantAdded, wantRemoved)
	}
}

func BenchmarkConcurrentBanlistReadsDuringSnapshot(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	client, err := NewSQLiteClient(ctx, b.TempDir(), "benchmark.db")
	if err != nil {
		b.Fatalf("new sqlite client: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })

	userIDs := make([]int64, 2_000)
	for i := range userIDs {
		userIDs[i] = int64(i + 1)
	}
	if _, _, err := client.ApplyBanlistSource(ctx, "benchmark", "daily", "initial", userIDs, time.Now(), nil, true); err != nil {
		b.Fatalf("seed banlist: %v", err)
	}

	writerDone := make(chan error, 1)
	go func() {
		generation := 0
		for ctx.Err() == nil {
			generation++
			_, _, err := client.ApplyBanlistSource(ctx, "benchmark", "daily", "refresh", userIDs, time.Now(), nil, true)
			if err != nil {
				if ctx.Err() != nil {
					writerDone <- nil
					return
				}
				writerDone <- err
				return
			}
		}
		writerDone <- nil
	}()

	readErr := make(chan error, 1)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := client.GetBanlist(ctx); err != nil {
				select {
				case readErr <- err:
				default:
				}
				return
			}
		}
	})
	b.StopTimer()
	cancel()
	if err := <-writerDone; err != nil && !errors.Is(err, context.Canceled) {
		b.Fatalf("refresh banlist: %v", err)
	}
	select {
	case err := <-readErr:
		b.Fatalf("read banlist: %v", err)
	default:
	}
}

func BenchmarkBanlistSnapshotApply100K(b *testing.B) {
	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, b.TempDir(), "benchmark.db")
	if err != nil {
		b.Fatalf("new sqlite client: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })

	userIDs := make([]int64, 100_000)
	for i := range userIDs {
		userIDs[i] = int64(i + 1)
	}
	if _, _, err := client.ApplyBanlistSource(ctx, "benchmark", "daily", "initial", userIDs, time.Now(), nil, true); err != nil {
		b.Fatalf("seed banlist: %v", err)
	}

	b.ResetTimer()
	for i := range b.N {
		generation := time.Now().Add(time.Duration(i)).Format(time.RFC3339Nano)
		if _, _, err := client.ApplyBanlistSource(ctx, "benchmark", "daily", generation, userIDs, time.Now(), nil, true); err != nil {
			b.Fatalf("apply banlist snapshot: %v", err)
		}
	}
}
