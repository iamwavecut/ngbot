package bot

import (
	"context"
	stdErrors "errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestGetUpdatesChansHealthyEmptyResponses(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	updates, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), testPollingOptions(), func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error) {
		if calls.Add(1) == 1 {
			return []api.Update{}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})

	select {
	case err := <-errs:
		t.Fatalf("unexpected polling error: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	select {
	case update := <-updates:
		t.Fatalf("unexpected update: %+v", update)
	default:
	}

	cancel()
	waitErrChannelClosed(t, errs)
}

func TestGetUpdatesChansRecoversAfterTransientErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	_, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), testPollingOptions(), func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error) {
		switch calls.Add(1) {
		case 1, 2:
			return nil, context.DeadlineExceeded
		case 3:
			return []api.Update{}, nil
		default:
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})

	select {
	case err := <-errs:
		t.Fatalf("unexpected polling error: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if calls.Load() < 3 {
		t.Fatalf("expected at least 3 polling attempts, got %d", calls.Load())
	}

	cancel()
	waitErrChannelClosed(t, errs)
}

func TestGetUpdatesChansFailsAfterRecoveryWindow(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	_, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), PollingOptions{
		RequestTimeout: 20 * time.Millisecond,
		RecoveryWindow: 25 * time.Millisecond,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}, func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error) {
		return nil, context.DeadlineExceeded
	})

	select {
	case err, ok := <-errs:
		if !ok {
			t.Fatal("expected polling recovery error")
		}
		var recoveryErr *PollingRecoveryError
		if !stdErrors.As(err, &recoveryErr) {
			t.Fatalf("expected PollingRecoveryError, got %T", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for polling recovery error")
	}
}

func TestGetUpdatesChansDeliversMalformedUpdatesToDurableFailurePolicy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	options := testPollingOptions()
	var persisted []int
	options.Persist = func(_ context.Context, update api.Update) error {
		persisted = append(persisted, update.UpdateID)
		return nil
	}
	updates, errs := getUpdatesChansWithFetcher(ctx, 2, api.NewUpdate(0), options, func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error) {
		if calls.Add(1) == 1 {
			return []api.Update{
				{UpdateID: 1},
				{
					UpdateID: 2,
					Message: &api.Message{
						Date: time.Now().Unix(),
					},
				},
			}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})

	select {
	case err := <-errs:
		t.Fatalf("unexpected polling error: %v", err)
	case update := <-updates:
		if update.UpdateID != 1 {
			t.Fatalf("expected malformed update 1 to reach durable policy, got %d", update.UpdateID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for valid update")
	}

	select {
	case update := <-updates:
		if update.UpdateID != 2 {
			t.Fatalf("expected valid update 2, got %d", update.UpdateID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for valid update")
	}
	if len(persisted) != 2 || persisted[0] != 1 || persisted[1] != 2 {
		t.Fatalf("persisted update IDs = %v, want [1 2]", persisted)
	}

	cancel()
	waitErrChannelClosed(t, errs)
}

func TestGetUpdatesChansDoesNotAdvanceOffsetUntilPersistenceSucceeds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	options := testPollingOptions()
	var persistCalls atomic.Int32
	options.Persist = func(_ context.Context, _ api.Update) error {
		if persistCalls.Add(1) == 1 {
			return stdErrors.New("database busy")
		}
		return nil
	}
	var mu sync.Mutex
	offsets := make([]int, 0, 2)
	updates, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), options, func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error) {
		mu.Lock()
		offsets = append(offsets, config.Offset)
		mu.Unlock()
		return []api.Update{messageUpdate(7, -7, 1)}, nil
	})

	select {
	case update := <-updates:
		if update.UpdateID != 7 {
			t.Fatalf("delivered update = %d, want 7", update.UpdateID)
		}
	case err := <-errs:
		t.Fatalf("unexpected polling error: %v", err)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for persisted update")
	}
	mu.Lock()
	gotOffsets := append([]int(nil), offsets...)
	mu.Unlock()
	if len(gotOffsets) < 2 || gotOffsets[0] != 0 || gotOffsets[1] != 0 {
		t.Fatalf("poll offsets before durable persistence = %v, want prefix [0 0]", gotOffsets)
	}
	cancel()
}

func TestGetUpdatesChansStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	_, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), testPollingOptions(), func(ctx context.Context, config api.UpdateConfig) ([]api.Update, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("polling fetcher did not start")
	}

	cancel()
	waitErrChannelClosed(t, errs)
}

func testPollingOptions() PollingOptions {
	return PollingOptions{
		RequestTimeout: 20 * time.Millisecond,
		RecoveryWindow: 80 * time.Millisecond,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
	}
}

func waitErrChannelClosed(t *testing.T, errs <-chan error) {
	t.Helper()

	select {
	case err, ok := <-errs:
		if ok {
			t.Fatalf("unexpected error while waiting for channel close: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for polling channel to close")
	}
}
