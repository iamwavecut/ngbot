package sqlite

import (
	"context"
	"errors"
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
	if err := client.ApplyBanlistSource(ctx, "lols", "daily", "lols-1", []int64{1, 2}, now, nil, true); err != nil {
		t.Fatalf("apply first lols snapshot: %v", err)
	}
	if err := client.ApplyBanlistSource(ctx, "cas", "daily", "cas-1", []int64{1, 3}, now, nil, true); err != nil {
		t.Fatalf("apply cas snapshot: %v", err)
	}
	if err := client.ApplyBanlistSource(ctx, "lols", "daily", "lols-2", []int64{2}, now.Add(time.Hour), nil, true); err != nil {
		t.Fatalf("replace lols snapshot: %v", err)
	}

	banlist, err := client.GetBanlist(ctx)
	if err != nil {
		t.Fatalf("get effective banlist: %v", err)
	}
	for _, userID := range []int64{1, 2, 3} {
		if _, ok := banlist[userID]; !ok {
			t.Fatalf("expected user %d to remain confirmed: %#v", userID, banlist)
		}
	}

	if err := client.ApplyBanlistSource(ctx, "cas", "daily", "cas-2", []int64{3}, now.Add(2*time.Hour), nil, true); err != nil {
		t.Fatalf("replace cas snapshot: %v", err)
	}
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
	if err := client.ApplyBanlistSource(ctx, "lols", "hourly", "hour-1", []int64{4}, now, &expiresAt, false); err != nil {
		t.Fatalf("apply hourly source: %v", err)
	}
	if err := client.ApplyBanlistSource(ctx, "maintenance", "expiry", "tick", nil, now.Add(27*time.Hour), nil, false); err != nil {
		t.Fatalf("rebuild after expiry: %v", err)
	}

	banlist, err := client.GetBanlist(ctx)
	if err != nil {
		t.Fatalf("get effective banlist: %v", err)
	}
	if _, ok := banlist[4]; ok {
		t.Fatalf("expected expired hourly source to be removed: %#v", banlist)
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
	if err := client.ApplyBanlistSource(ctx, "benchmark", "daily", "initial", userIDs, time.Now(), nil, true); err != nil {
		b.Fatalf("seed banlist: %v", err)
	}

	writerDone := make(chan error, 1)
	go func() {
		generation := 0
		for ctx.Err() == nil {
			generation++
			err := client.ApplyBanlistSource(ctx, "benchmark", "daily", "refresh", userIDs, time.Now(), nil, true)
			if err != nil {
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
