package bot

import (
	"context"
	stdErrors "errors"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const pollingConflictTestCalls = 50

func conflictingFetcher(failure error, calls *atomic.Int64) updateFetcher {
	return func(ctx context.Context, _ api.UpdateConfig) ([]api.Update, error) {
		switch call := calls.Add(1); {
		case call <= pollingConflictTestCalls:
			return nil, failure
		case call == pollingConflictTestCalls+1:
			return []api.Update{{UpdateID: 1}}, nil
		default:
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
}

func conflictTestOptions() PollingOptions {
	return PollingOptions{
		RequestTimeout: time.Second,
		RecoveryWindow: 5 * time.Millisecond,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}
}

func TestPollingConflictDoesNotExhaustRecoveryWindow(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int64
	conflict := &api.Error{Code: 409, Message: "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}
	updates, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), conflictTestOptions(), conflictingFetcher(conflict, &calls))

	select {
	case update := <-updates:
		if update.UpdateID != 1 {
			t.Fatalf("update = %d, want 1", update.UpdateID)
		}
	case err := <-errs:
		t.Fatalf("conflicts exhausted the recovery window: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the update after conflicts")
	}
	cancel()
	if err, ok := <-errs; ok {
		t.Fatalf("unexpected polling error: %v", err)
	}
}

func TestPollingNonConflictErrorStillExhaustsRecoveryWindow(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	_, errs := getUpdatesChansWithFetcher(t.Context(), 1, api.NewUpdate(0), conflictTestOptions(), conflictingFetcher(stdErrors.New("network down"), &calls))

	select {
	case err, ok := <-errs:
		var recoveryErr *PollingRecoveryError
		if !ok || !stdErrors.As(err, &recoveryErr) {
			t.Fatalf("polling error = %v, want PollingRecoveryError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for polling recovery error")
	}
}

func TestPollingConflictLogIsRateLimited(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks) })
	hook := logtest.NewLocal(logger)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int64
	conflict := api.Error{Code: 409, Message: "Conflict: rate limit probe"}
	updates, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), conflictTestOptions(), conflictingFetcher(conflict, &calls))
	select {
	case <-updates:
	case err := <-errs:
		t.Fatalf("unexpected polling error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the update after conflicts")
	}
	cancel()
	for range errs {
	}

	var conflictErrors []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.ErrorLevel && entry.Message == pollingConflictLogMessage && entry.Data[log.ErrorKey] == conflict {
			conflictErrors = append(conflictErrors, entry)
		}
	}
	if len(conflictErrors) != 1 {
		t.Fatalf("conflict ERROR entries = %d, want 1", len(conflictErrors))
	}
	if _, ok := conflictErrors[0].Data["conflicts"]; !ok {
		t.Fatalf("conflict ERROR fields = %v, want conflicts", conflictErrors[0].Data)
	}
}

func TestPollingWebhookConflictNamesTheWebhook(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks) })
	hook := logtest.NewLocal(logger)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int64
	conflict := &api.Error{Code: 409, Message: "Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first"}
	updates, errs := getUpdatesChansWithFetcher(ctx, 1, api.NewUpdate(0), conflictTestOptions(), conflictingFetcher(conflict, &calls))
	select {
	case <-updates:
	case err := <-errs:
		t.Fatalf("unexpected polling error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the update after conflicts")
	}
	cancel()
	for range errs {
	}

	var messages []string
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.ErrorLevel && entry.Data[log.ErrorKey] == conflict {
			messages = append(messages, entry.Message)
		}
	}
	if len(messages) != 1 || messages[0] != pollingWebhookConflictLogMessage {
		t.Fatalf("conflict ERROR messages = %q, want one %q", messages, pollingWebhookConflictLogMessage)
	}
}
