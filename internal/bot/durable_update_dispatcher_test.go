package bot

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
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
	waitForCondition(t, func() bool {
		failures, listErr := store.ListTelegramUpdateFailures(t.Context(), 10)
		return listErr == nil && len(failures) == 1 && failures[0].FailureReason == "retry_exhausted"
	})
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
	if err := dispatcher.Submit(t.Context(), second); err != nil {
		t.Fatalf("notify scheduler for persisted second update: %v", err)
	}
	updates, err := store.ListRunnableTelegramUpdates(t.Context(), time.Now(), 10)
	if err != nil {
		t.Fatalf("list persisted updates: %v", err)
	}
	foundSecond := false
	for _, persisted := range updates {
		foundSecond = foundSecond || persisted.UpdateID == second.UpdateID
	}
	if !foundSecond {
		t.Fatalf("saturated second update was not durable: %#v", updates)
	}
}

func TestDurableUpdateDispatcherWakesForFutureRetryAfterRestart(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	update := messageUpdate(130, -130, 1)
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	now := time.Now()
	inserted, err := store.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: update.UpdateID, DispatchKey: updateDispatchKey(&update), Payload: payload, ReceivedAt: now,
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue future retry: inserted=%t err=%v", inserted, err)
	}
	claimedUpdate, claimed, claimErr := store.ClaimTelegramUpdate(t.Context(), update.UpdateID, "restart-owner", now, now.Add(time.Minute))
	if claimErr != nil || !claimed {
		t.Fatalf("claim future retry: claimed=%t err=%v", claimed, claimErr)
	}
	dueAt := now.Add(80 * time.Millisecond)
	if changed, retryErr := store.ScheduleTelegramUpdateRetry(t.Context(), update.UpdateID, claimedUpdate.LeaseOwner, claimedUpdate.LeaseVersion, dueAt, "llm", "unavailable"); retryErr != nil || !changed {
		t.Fatalf("schedule future retry: changed=%t err=%v", changed, retryErr)
	}

	processed := make(chan time.Time, 1)
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		processed <- time.Now()
		return nil
	}, nil, testDurableDispatcherOptions(), nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	select {
	case at := <-processed:
		t.Fatalf("future retry ran %s early", dueAt.Sub(at))
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case at := <-processed:
		if at.Before(dueAt) {
			t.Fatalf("future retry ran before available_at: at=%s due=%s", at, dueAt)
		}
	case <-time.After(time.Second):
		t.Fatal("future retry was not recovered after restart")
	}
}

func TestDurableUpdateDispatcherUsesOneSchedulerDuringManyChatOutage(t *testing.T) {
	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const updateCount = 4096
	var attempts atomic.Int32
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		attempts.Add(1)
		return NewRetryableUpdateFailure(UpdateFailureLLM, "provider_error", errors.New("outage"))
	}, nil, DurableUpdateDispatcherOptions{
		MaxWorkers: 8, PendingBudget: 32, MaxAttempts: 2,
		InitialBackoff: time.Hour, MaxBackoff: time.Hour,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	for i := range updateCount {
		update := messageUpdate(10_000+i, int64(-10_000-i), i+1)
		if err := dispatcher.Persist(t.Context(), update); err != nil {
			t.Fatalf("persist outage update %d: %v", i, err)
		}
		if err := dispatcher.Submit(t.Context(), update); err != nil {
			t.Fatalf("submit outage update %d: %v", i, err)
		}
	}
	waitForConditionTimeout(t, 15*time.Second, func() bool { return attempts.Load() == updateCount })
	stack := make([]byte, 8<<20)
	stack = stack[:runtime.Stack(stack, true)]
	if count := strings.Count(string(stack), "(*DurableUpdateDispatcher).scheduleRetry"); count != 0 {
		t.Fatalf("outage created %d per-retry timer goroutines", count)
	}
	dispatcher.mu.Lock()
	scheduled := len(dispatcher.scheduled)
	dispatcher.mu.Unlock()
	if scheduled > 32 {
		t.Fatalf("scheduled in-memory updates = %d, want <= 32", scheduled)
	}
}

func TestDurableUpdateDispatcherCleansRetentionWhileRunning(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error { return nil }, nil, DurableUpdateDispatcherOptions{
		MaxWorkers: 1, PendingBudget: 1, CleanupInterval: 10 * time.Millisecond,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })

	old := time.Now().Add(-40 * 24 * time.Hour)
	update := messageUpdate(140, -140, 1)
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal retained update: %v", err)
	}
	inserted, err := store.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: update.UpdateID, DispatchKey: updateDispatchKey(&update), Payload: payload, ReceivedAt: old,
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue retained update: inserted=%t err=%v", inserted, err)
	}
	now := time.Now()
	claimedUpdate, claimed, claimErr := store.ClaimTelegramUpdate(t.Context(), update.UpdateID, "retention-owner", now, now.Add(time.Minute))
	if claimErr != nil || !claimed {
		t.Fatalf("claim retained update: claimed=%t err=%v", claimed, claimErr)
	}
	if changed, completeErr := store.CompleteTelegramUpdate(t.Context(), update.UpdateID, claimedUpdate.LeaseOwner, claimedUpdate.LeaseVersion, "handler", old); completeErr != nil || !changed {
		t.Fatalf("complete retained update: changed=%t err=%v", changed, completeErr)
	}
	waitForConditionTimeout(t, time.Second, func() bool {
		updates, listErr := store.ListRunnableTelegramUpdates(t.Context(), time.Now(), 10)
		if listErr != nil || len(updates) != 0 {
			return false
		}
		stored, found, getErr := store.TelegramUpdate(t.Context(), update.UpdateID)
		return getErr == nil && !found && stored == nil
	})
}

func TestDurableUpdateDispatcherRetriesBusyInboxTransitions(t *testing.T) {
	t.Parallel()

	for _, transition := range []string{"claim", "complete", "dead_letter"} {
		t.Run(transition, func(t *testing.T) {
			t.Parallel()
			base, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			store := &busyTransitionStore{DurableUpdateStore: base, transition: transition, remaining: 2}
			dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
				if transition == "dead_letter" {
					return NewTerminalUpdateFailure(UpdateFailurePayload, "poison", errors.New("poison"))
				}
				return nil
			}, nil, DurableUpdateDispatcherOptions{
				MaxWorkers: 1, PendingBudget: 1, MaxAttempts: 1,
				SchedulerBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
				RecoveryInterval: 5 * time.Millisecond, ProcessingTimeout: 50 * time.Millisecond,
			}, nil)
			if err := dispatcher.Start(t.Context()); err != nil {
				t.Fatalf("start dispatcher: %v", err)
			}
			t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
			update := messageUpdate(200, -200, 1)
			if err := dispatcher.Persist(t.Context(), update); err != nil {
				t.Fatalf("persist update: %v", err)
			}
			if err := dispatcher.Submit(t.Context(), update); err != nil {
				t.Fatalf("submit update: %v", err)
			}
			waitForConditionTimeout(t, time.Second, func() bool {
				record, found, recordErr := base.TelegramUpdate(t.Context(), update.UpdateID)
				if recordErr != nil || !found {
					return false
				}
				if transition == "dead_letter" {
					return record.Status == db.TelegramUpdateStatusDeadLetter
				}
				return record.Status == db.TelegramUpdateStatusCompleted
			})
			if store.calls.Load() < 3 {
				t.Fatalf("%s transition calls = %d, want at least 3", transition, store.calls.Load())
			}
		})
	}
}

func TestDurableUpdateDispatcherKeepsLeaseThroughBusyCompletion(t *testing.T) {
	t.Parallel()

	base, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	const processingTimeout = 500 * time.Millisecond
	store := &busyCompletionStore{
		DurableUpdateStore: base,
		busyFor:            3 * processingTimeout,
	}
	var handlerCalls atomic.Int32
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		handlerCalls.Add(1)
		return nil
	}, nil, DurableUpdateDispatcherOptions{
		MaxWorkers: 2, PendingBudget: 2, MaxAttempts: 2,
		ProcessingTimeout: processingTimeout, RecoveryInterval: 2 * time.Millisecond,
		SchedulerBackoff: 2 * time.Millisecond, MaxBackoff: 2 * time.Millisecond,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	update := messageUpdate(201, -201, 1)
	if err := dispatcher.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist update: %v", err)
	}
	waitForConditionTimeout(t, 5*time.Second, func() bool {
		record, found, recordErr := base.TelegramUpdate(t.Context(), update.UpdateID)
		return recordErr == nil && found && record.Status == db.TelegramUpdateStatusCompleted
	})
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("handler side effects = %d, want 1", got)
	}
	if store.completeCalls.Load() < 2 {
		t.Fatalf("completion calls = %d, want retries", store.completeCalls.Load())
	}
}

func TestDurableUpdateDispatcherKeepsLeaseThroughBusyFailureTransitions(t *testing.T) {
	for _, test := range []struct {
		name         string
		finalStatus  string
		handlerCalls int32
	}{
		{name: "retry", finalStatus: db.TelegramUpdateStatusCompleted, handlerCalls: 2},
		{name: "dead_letter", finalStatus: db.TelegramUpdateStatusDeadLetter, handlerCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })
			const processingTimeout = 500 * time.Millisecond
			store := &busyFailureTransitionStore{
				DurableUpdateStore: base,
				transition:         test.name,
				busyFor:            3 * processingTimeout,
			}
			var calls atomic.Int32
			dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
				call := calls.Add(1)
				if test.name == "retry" && call == 1 {
					return NewRetryableUpdateFailure(UpdateFailureTelegram, "temporary", errors.New("temporary"))
				}
				if test.name == "dead_letter" {
					return NewTerminalUpdateFailure(UpdateFailurePayload, "poison", errors.New("poison"))
				}
				return nil
			}, nil, DurableUpdateDispatcherOptions{
				MaxWorkers: 2, PendingBudget: 2, MaxAttempts: 3,
				InitialBackoff: time.Millisecond, ProcessingTimeout: processingTimeout,
				RecoveryInterval: 2 * time.Millisecond, SchedulerBackoff: 2 * time.Millisecond,
				MaxBackoff: 2 * time.Millisecond,
			}, nil)
			if err := dispatcher.Start(t.Context()); err != nil {
				t.Fatalf("start dispatcher: %v", err)
			}
			t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
			update := messageUpdate(202, -202, 1)
			if err := dispatcher.Persist(t.Context(), update); err != nil {
				t.Fatalf("persist update: %v", err)
			}
			waitForConditionTimeout(t, 5*time.Second, func() bool {
				record, found, recordErr := base.TelegramUpdate(t.Context(), update.UpdateID)
				return recordErr == nil && found && record.Status == test.finalStatus
			})
			if got := calls.Load(); got != test.handlerCalls {
				t.Fatalf("handler calls = %d, want %d", got, test.handlerCalls)
			}
			if !store.persisted.Load() {
				t.Fatalf("%s outcome was not persisted by its original lease owner", test.name)
			}
		})
	}
}

func TestDurableUpdateDispatcherHeartbeatsLongRunningHandler(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}, nil, DurableUpdateDispatcherOptions{
		MaxWorkers: 2, PendingBudget: 2, MaxAttempts: 2,
		ProcessingTimeout: 30 * time.Millisecond, RecoveryInterval: 5 * time.Millisecond,
		SchedulerBackoff: time.Millisecond,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	update := messageUpdate(220, -220, 1)
	if err := dispatcher.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist update: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	waitForConditionTimeout(t, time.Second, func() bool {
		record, found, recordErr := store.TelegramUpdate(t.Context(), update.UpdateID)
		return recordErr == nil && found && record.Status == db.TelegramUpdateStatusCompleted
	})
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestDurableUpdateDispatcherRecoversExpiredCrashLeaseAfterRestart(t *testing.T) {
	t.Parallel()

	store, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	update := messageUpdate(221, -221, 1)
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	now := time.Now()
	leaseUntil := now.Add(60 * time.Millisecond)
	if inserted, enqueueErr := store.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: update.UpdateID, DispatchKey: updateDispatchKey(&update), Payload: payload, ReceivedAt: now,
	}); enqueueErr != nil || !inserted {
		t.Fatalf("enqueue update: inserted=%t err=%v", inserted, enqueueErr)
	}
	if _, claimed, claimErr := store.ClaimTelegramUpdate(t.Context(), update.UpdateID, "crashed-owner", now, leaseUntil); claimErr != nil || !claimed {
		t.Fatalf("claim crashed update: claimed=%t err=%v", claimed, claimErr)
	}
	processed := make(chan time.Time, 1)
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		processed <- time.Now()
		return nil
	}, nil, DurableUpdateDispatcherOptions{
		MaxWorkers: 1, PendingBudget: 1, MaxAttempts: 2,
		ProcessingTimeout: 30 * time.Millisecond, RecoveryInterval: 5 * time.Millisecond,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	select {
	case at := <-processed:
		t.Fatalf("crashed lease recovered before expiry: at=%s lease=%s", at, leaseUntil)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case at := <-processed:
		if at.Before(leaseUntil) {
			t.Fatalf("crashed lease recovered early: at=%s lease=%s", at, leaseUntil)
		}
	case <-time.After(time.Second):
		t.Fatal("expired crash lease was not recovered")
	}
}

func TestDurableUpdateDispatcherBusyHeartbeatCannotRenewPastExpiryOrOverlapRecovery(t *testing.T) {
	t.Parallel()

	base, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	store := &busyHeartbeatStore{DurableUpdateStore: base}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	dispatcher := NewDurableUpdateDispatcher(store, func(context.Context, *api.Update) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maxActive.Load()
			if current <= observed || maxActive.CompareAndSwap(observed, current) {
				break
			}
		}
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		close(secondStarted)
		return nil
	}, nil, DurableUpdateDispatcherOptions{
		MaxWorkers: 2, PendingBudget: 2, MaxAttempts: 2,
		ProcessingTimeout: 30 * time.Millisecond, RecoveryInterval: 2 * time.Millisecond,
		SchedulerBackoff: 3 * time.Millisecond, MaxBackoff: 3 * time.Millisecond,
	}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	update := messageUpdate(222, -222, 1)
	if err := dispatcher.Persist(t.Context(), update); err != nil {
		t.Fatalf("persist update: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first handler did not start")
	}
	time.Sleep(70 * time.Millisecond)
	renewCalls := store.renewCalls.Load()
	time.Sleep(20 * time.Millisecond)
	if store.renewCalls.Load() != renewCalls {
		t.Fatalf("heartbeat kept retrying after lease expiry: before=%d after=%d", renewCalls, store.renewCalls.Load())
	}
	select {
	case <-secondStarted:
		t.Fatal("recovery overlapped the still-running stale owner")
	default:
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("recovery owner did not run after stale handler exited")
	}
	if maxActive.Load() != 1 {
		t.Fatalf("maximum concurrent executions = %d, want 1", maxActive.Load())
	}
}

type busyHeartbeatStore struct {
	DurableUpdateStore
	renewCalls atomic.Int32
}

func (s *busyHeartbeatStore) RenewTelegramUpdateLease(context.Context, int, string, int64, time.Time, time.Time) (bool, error) {
	s.renewCalls.Add(1)
	return false, codedSQLiteError{code: 5}
}

type busyCompletionStore struct {
	DurableUpdateStore
	busyFor       time.Duration
	busyUntil     atomic.Int64
	completeCalls atomic.Int32
}

func (s *busyCompletionStore) CompleteTelegramUpdate(ctx context.Context, updateID int, owner string, version int64, source string, now time.Time) (bool, error) {
	s.completeCalls.Add(1)
	deadline := s.busyUntil.Load()
	if deadline == 0 {
		deadline = time.Now().Add(s.busyFor).UnixNano()
		if !s.busyUntil.CompareAndSwap(0, deadline) {
			deadline = s.busyUntil.Load()
		}
	}
	if time.Now().UnixNano() < deadline {
		return false, codedSQLiteError{code: 5}
	}
	return s.DurableUpdateStore.CompleteTelegramUpdate(ctx, updateID, owner, version, source, now)
}

type busyFailureTransitionStore struct {
	DurableUpdateStore
	transition string
	busyFor    time.Duration
	busyUntil  atomic.Int64
	persisted  atomic.Bool
}

func (s *busyFailureTransitionStore) ScheduleTelegramUpdateRetry(ctx context.Context, updateID int, owner string, version int64, nextAttemptAt time.Time, source, lastError string) (bool, error) {
	if s.transition == "retry" && s.busy() {
		return false, codedSQLiteError{code: 5}
	}
	changed, err := s.DurableUpdateStore.ScheduleTelegramUpdateRetry(ctx, updateID, owner, version, nextAttemptAt, source, lastError)
	if changed {
		s.persisted.Store(true)
	}
	return changed, err
}

func (s *busyFailureTransitionStore) DeadLetterTelegramUpdate(ctx context.Context, updateID int, owner string, version int64, source, reason, lastError string, now time.Time) (bool, error) {
	if s.transition == "dead_letter" && s.busy() {
		return false, codedSQLiteError{code: 5}
	}
	changed, err := s.DurableUpdateStore.DeadLetterTelegramUpdate(ctx, updateID, owner, version, source, reason, lastError, now)
	if changed {
		s.persisted.Store(true)
	}
	return changed, err
}

func (s *busyFailureTransitionStore) busy() bool {
	deadline := s.busyUntil.Load()
	if deadline == 0 {
		deadline = time.Now().Add(s.busyFor).UnixNano()
		if !s.busyUntil.CompareAndSwap(0, deadline) {
			deadline = s.busyUntil.Load()
		}
	}
	return time.Now().UnixNano() < deadline
}

type busyTransitionStore struct {
	DurableUpdateStore
	transition string
	remaining  int32
	calls      atomic.Int32
}

func (s *busyTransitionStore) fail(name string) error {
	if s.transition != name {
		return nil
	}
	s.calls.Add(1)
	if s.remaining > 0 {
		s.remaining--
		return codedSQLiteError{code: 5}
	}
	return nil
}

func (s *busyTransitionStore) ClaimTelegramUpdate(ctx context.Context, updateID int, owner string, now, leaseUntil time.Time) (*db.TelegramUpdate, bool, error) {
	if err := s.fail("claim"); err != nil {
		return nil, false, err
	}
	return s.DurableUpdateStore.ClaimTelegramUpdate(ctx, updateID, owner, now, leaseUntil)
}

func (s *busyTransitionStore) CompleteTelegramUpdate(ctx context.Context, updateID int, owner string, version int64, source string, now time.Time) (bool, error) {
	if err := s.fail("complete"); err != nil {
		return false, err
	}
	return s.DurableUpdateStore.CompleteTelegramUpdate(ctx, updateID, owner, version, source, now)
}

func (s *busyTransitionStore) DeadLetterTelegramUpdate(ctx context.Context, updateID int, owner string, version int64, source, reason, lastError string, now time.Time) (bool, error) {
	if err := s.fail("dead_letter"); err != nil {
		return false, err
	}
	return s.DurableUpdateStore.DeadLetterTelegramUpdate(ctx, updateID, owner, version, source, reason, lastError, now)
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

func waitForConditionTimeout(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
