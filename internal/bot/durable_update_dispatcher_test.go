package bot

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

func TestDurableUpdateDispatcherDeduplicatesAndRecoversRestart(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	update := messageUpdate(101, -100, 1)
	first := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		t.Fatal("persisted update executed before restart")
		return nil
	}, nil, testDurableDispatcherOptions(), nil)
	if err := first.Start(t.Context()); err != nil {
		t.Fatalf("start first dispatcher: %v", err)
	}
	if err := first.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist update: %v", err)
	}
	if err := first.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist duplicate update: %v", err)
	}
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("stop first dispatcher: %v", err)
	}

	var calls atomic.Int32
	processed := make(chan struct{})
	second := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		if calls.Add(1) == 1 {
			close(processed)
		}
		return nil
	}, nil, testDurableDispatcherOptions(), nil)
	if err := second.Start(t.Context()); err != nil {
		t.Fatalf("start restarted dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = second.Stop(context.Background()) })
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("restart did not recover persisted update")
	}
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("duplicate update executions = %d, want 1", got)
	}
}

func TestDurableUpdateDispatcherRetriesHeadWithoutReorderingChat(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var mu sync.Mutex
	order := make([]int, 0, 4)
	firstAttempts := 0
	done := make(chan struct{})
	dispatcher := NewDurableUpdateDispatcher(store, func(_ context.Context, update *api.Update) error {
		mu.Lock()
		order = append(order, update.UpdateID)
		if update.UpdateID == 1 {
			firstAttempts++
			attempt := firstAttempts
			mu.Unlock()
			if attempt < 3 {
				return NewRetryableUpdateFailure(UpdateFailureSQLite, "database_busy", errors.New("busy"))
			}
			return nil
		}
		mu.Unlock()
		close(done)
		return nil
	}, nil, testDurableDispatcherOptions(), nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })

	for _, update := range []api.Update{messageUpdate(1, -10, 1), messageUpdate(2, -10, 2)} {
		if err := dispatcher.Persist(t.Context(), update); err != nil {
			t.Fatalf("persist %d: %v", update.UpdateID, err)
		}
		if err := dispatcher.Submit(t.Context(), update); err != nil {
			t.Fatalf("submit %d: %v", update.UpdateID, err)
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("same-chat tail did not run after head retry")
	}
	mu.Lock()
	got := slices.Clone(order)
	mu.Unlock()
	if !slices.Equal(got, []int{1, 1, 1, 2}) {
		t.Fatalf("processing order = %v, want [1 1 1 2]", got)
	}
}

func TestDurableUpdateDispatcherDeadLettersPanicWithoutRepeatingSideEffect(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var effects atomic.Int32
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		effects.Add(1)
		panic("crash after side effect")
	}, nil, testDurableDispatcherOptions(), nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	update := messageUpdate(88, -88, 1)
	if err := dispatcher.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist update: %v", err)
	}
	if err := dispatcher.Submit(t.Context(), update); err != nil {
		t.Fatalf("submit update: %v", err)
	}
	waitForCondition(t, func() bool {
		failures, listErr := store.ListTelegramUpdateFailures(t.Context(), 10)
		return listErr == nil && len(failures) == 1
	})
	if got := effects.Load(); got != 1 {
		t.Fatalf("ambiguous side effect repeated %d times", got)
	}
}

func TestDurableUpdateDispatcherDegradesAfterBoundedLLMRetries(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var attempts atomic.Int32
	var degradations atomic.Int32
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		attempts.Add(1)
		return NewRetryableUpdateFailure(UpdateFailureLLM, "provider_error", errors.New("unavailable"))
	}, func(context.Context, *api.Update, UpdateFailure) error {
		degradations.Add(1)
		return nil
	}, DurableUpdateDispatcherOptions{
		MaxWorkers:     1,
		PendingBudget:  2,
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	update := messageUpdate(90, -90, 1)
	if err := dispatcher.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist update: %v", err)
	}
	if err := dispatcher.Submit(t.Context(), update); err != nil {
		t.Fatalf("submit update: %v", err)
	}
	waitForCondition(t, func() bool { return degradations.Load() == 1 })
	if attempts.Load() != 3 || degradations.Load() != 1 {
		t.Fatalf("attempts=%d degradations=%d, want 3 and 1", attempts.Load(), degradations.Load())
	}
	failures, err := store.ListTelegramUpdateFailures(t.Context(), 10)
	if err != nil || len(failures) != 1 || failures[0].FailureReason != "retry_exhausted" {
		t.Fatalf("exhausted failure ledger = %#v err=%v", failures, err)
	}
}

func TestDurableUpdateDispatcherKeepsPersistedUpdateWhenQueueIsSaturated(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	release := make(chan struct{})
	dispatcher := NewDurableUpdateDispatcher(store, func(ctx context.Context, _ *api.Update) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil, DurableUpdateDispatcherOptions{MaxWorkers: 1, PendingBudget: 1, MaxAttempts: 1}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() {
		close(release)
		_ = dispatcher.Stop(context.Background())
	})
	first := messageUpdate(120, -120, 1)
	second := messageUpdate(121, -121, 2)
	for _, update := range []api.Update{first, second} {
		if err := dispatcher.Persist(t.Context(), update); err != nil {
			t.Fatalf("persist %d: %v", update.UpdateID, err)
		}
	}
	if err := dispatcher.Submit(t.Context(), first); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	submitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := dispatcher.Submit(submitCtx, second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated submit error = %v, want deadline exceeded", err)
	}
	updates, err := store.ListRunnableTelegramUpdates(t.Context(), time.Now(), 10)
	if err != nil {
		t.Fatalf("list persisted updates: %v", err)
	}
	if len(updates) != 1 || updates[0].UpdateID != second.UpdateID {
		t.Fatalf("saturated persisted updates = %#v", updates)
	}
}

func testDurableDispatcherOptions() DurableUpdateDispatcherOptions {
	return DurableUpdateDispatcherOptions{
		MaxWorkers:     2,
		PendingBudget:  8,
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}
}
