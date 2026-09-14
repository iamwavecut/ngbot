package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestSenderChatLLMExhaustionRemainsInDurableFailureQueue(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	f.detector.err = errors.New("provider unavailable")
	f.detector.result = nil
	store := f.store.(interface {
		bot.DurableUpdateStore
		ListTelegramUpdateFailures(context.Context, int) ([]*db.TelegramUpdateFailure, error)
	})
	router := NewModerationRouter(nil, f.reactor)
	dispatcher := bot.NewDurableUpdateDispatcher(store, func(ctx context.Context, update *api.Update) error {
		_, err := router.Handle(ctx, update, &update.Message.Chat, update.Message.From)
		return err
	}, func(ctx context.Context, update *api.Update, failure bot.UpdateFailure) error {
		return router.HandleExhaustedUpdateFailure(ctx, update, &update.Message.Chat, update.Message.From, failure)
	}, bot.DurableUpdateDispatcherOptions{MaxWorkers: 1, PendingBudget: 1, MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, nil)
	if err := dispatcher.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dispatcher.Stop(context.Background()) })
	message := f.message(1)
	message.SenderChat = &api.Chat{ID: -300, Type: "channel"}
	update := api.Update{UpdateID: 123, Message: message}
	if err := dispatcher.Persist(t.Context(), update); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Submit(t.Context(), update); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		failures, err := store.ListTelegramUpdateFailures(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) == 1 {
			if failures[0].UpdateID != update.UpdateID || failures[0].FailureReason != "retry_exhausted" {
				t.Fatalf("wrong durable failure: %#v", failures[0])
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("channel failure did not reach durable queue")
		case <-tick.C:
		}
	}
	if err := dispatcher.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	ban := f.reactor.banService.(*testBanService)
	if f.detector.calls != 3 || ban.muteCalls != 0 || len(ban.bans) != 0 {
		t.Fatalf("exhaustion calls=%d mutes=%d bans=%v", f.detector.calls, ban.muteCalls, ban.bans)
	}
	trust, err := f.store.MessageTrust(t.Context(), f.chat.ID, db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300})
	if err != nil || trust == nil || trust.SafeMessages != 0 || trust.Trusted(f.now) {
		t.Fatalf("failed channel gained trust: %#v error=%v", trust, err)
	}
}
