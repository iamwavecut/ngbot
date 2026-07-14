package bot

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestKeyedDispatcherPreservesChatFIFOAndAllowsOtherChatsToProgress(t *testing.T) {
	t.Parallel()

	firstChatStarted := make(chan struct{})
	releaseFirstChat := make(chan struct{})
	otherChatDone := make(chan struct{})
	var mu sync.Mutex
	order := make([]int, 0, 3)
	dispatcher := NewKeyedDispatcher(func(ctx context.Context, update *api.Update) error {
		if update.UpdateID == 1 {
			close(firstChatStarted)
			select {
			case <-releaseFirstChat:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		mu.Lock()
		order = append(order, update.UpdateID)
		mu.Unlock()
		if update.UpdateID == 3 {
			close(otherChatDone)
		}
		return nil
	}, 8, 100, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })

	for _, update := range []api.Update{
		messageUpdate(1, 100, 10),
		messageUpdate(2, 100, 11),
		messageUpdate(3, 200, 12),
	} {
		if err := dispatcher.Submit(t.Context(), update); err != nil {
			t.Fatalf("submit update %d: %v", update.UpdateID, err)
		}
	}
	select {
	case <-firstChatStarted:
	case <-time.After(time.Second):
		t.Fatal("first chat did not start")
	}
	select {
	case <-otherChatDone:
	case <-time.After(time.Second):
		t.Fatal("other chat did not progress while first chat was blocked")
	}
	close(releaseFirstChat)
	waitForCondition(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 3
	})
	mu.Lock()
	got := append([]int(nil), order...)
	mu.Unlock()
	if slices.Index(got, 1) > slices.Index(got, 2) {
		t.Fatalf("same-chat order was not FIFO: %v", got)
	}
}

func TestKeyedDispatcherCapsActiveWorkers(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	dispatcher := NewKeyedDispatcher(func(ctx context.Context, update *api.Update) error {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		defer active.Add(-1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, 2, 10, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if err := dispatcher.Submit(t.Context(), messageUpdate(i, int64(i), i)); err != nil {
			t.Fatalf("submit update: %v", err)
		}
	}
	waitForCondition(t, func() bool { return maximum.Load() == 2 })
	close(release)
	if err := dispatcher.Stop(context.Background()); err != nil {
		t.Fatalf("stop dispatcher: %v", err)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("active workers = %d, want 2", got)
	}
}

func TestKeyedDispatcherBackpressuresAtPendingBudget(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	dispatcher := NewKeyedDispatcher(func(ctx context.Context, update *api.Update) error {
		if update.UpdateID == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, 1, 1, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	if err := dispatcher.Submit(t.Context(), messageUpdate(1, 100, 1)); err != nil {
		t.Fatalf("submit first update: %v", err)
	}
	<-started
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- dispatcher.Submit(t.Context(), messageUpdate(2, 200, 2))
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second submit bypassed backpressure: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second submit after capacity released: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second submit stayed blocked after capacity released")
	}
	if err := dispatcher.Stop(context.Background()); err != nil {
		t.Fatalf("stop dispatcher: %v", err)
	}
}

func TestKeyedDispatcherRecoversPanicsAndContinuesKey(t *testing.T) {
	t.Parallel()

	processed := make(chan int, 1)
	dispatcher := NewKeyedDispatcher(func(ctx context.Context, update *api.Update) error {
		if update.UpdateID == 1 {
			panic("boom")
		}
		processed <- update.UpdateID
		return nil
	}, 1, 2, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	if err := dispatcher.Submit(t.Context(), messageUpdate(1, 100, 1)); err != nil {
		t.Fatalf("submit panic update: %v", err)
	}
	if err := dispatcher.Submit(t.Context(), messageUpdate(2, 100, 2)); err != nil {
		t.Fatalf("submit following update: %v", err)
	}
	select {
	case got := <-processed:
		if got != 2 {
			t.Fatalf("processed update = %d, want 2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("same key did not continue after panic")
	}
	if err := dispatcher.Stop(context.Background()); err != nil {
		t.Fatalf("stop dispatcher: %v", err)
	}
}

func messageUpdate(updateID int, chatID int64, messageID int) api.Update {
	return api.Update{
		UpdateID: updateID,
		Message: &api.Message{
			MessageID: messageID,
			Chat:      api.Chat{ID: chatID, Type: "supergroup"},
			From:      &api.User{ID: int64(messageID)},
			Date:      time.Now().Unix(),
		},
	}
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
