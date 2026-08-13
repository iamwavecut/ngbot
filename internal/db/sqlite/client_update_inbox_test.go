package sqlite

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestTelegramUpdateInboxMigrationCreatesDurableQueue(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	for _, table := range []string{"telegram_update_inbox", "telegram_update_failures"} {
		var count int
		if err := client.db.GetContext(t.Context(), &count, `
			SELECT COUNT(*)
			FROM sqlite_master
			WHERE type = 'table' AND name = ?
		`, table); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
}

func TestTelegramUpdateInboxDeduplicatesAndPreservesOriginalPayload(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	receivedAt := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	inserted, err := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID:         100,
		DispatchKey:      "chat:-10",
		Payload:          []byte(`{"update_id":100,"message":{"text":"first"}}`),
		SecurityRelevant: true,
		ReceivedAt:       receivedAt,
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue first update: inserted=%t err=%v", inserted, err)
	}
	inserted, err = client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID:    100,
		DispatchKey: "chat:-20",
		Payload:     []byte(`{"update_id":100,"message":{"text":"replacement"}}`),
		ReceivedAt:  receivedAt.Add(time.Minute),
	})
	if err != nil || inserted {
		t.Fatalf("enqueue duplicate update: inserted=%t err=%v", inserted, err)
	}

	updates, err := client.ListRunnableTelegramUpdates(t.Context(), receivedAt, 10)
	if err != nil {
		t.Fatalf("list runnable updates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("runnable update count = %d, want 1", len(updates))
	}
	if updates[0].DispatchKey != "chat:-10" || !bytes.Contains(updates[0].Payload, []byte(`"first"`)) || !updates[0].SecurityRelevant {
		t.Fatalf("duplicate changed original update: %#v", updates[0])
	}
}

func TestTelegramUpdateInboxBlocksLaterChatUpdateThroughRetry(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	for _, updateID := range []int{10, 11} {
		inserted, enqueueErr := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
			UpdateID:    updateID,
			DispatchKey: "chat:-10",
			Payload:     []byte(`{"update_id":10}`),
			ReceivedAt:  now,
		})
		if enqueueErr != nil || !inserted {
			t.Fatalf("enqueue %d: inserted=%t err=%v", updateID, inserted, enqueueErr)
		}
	}

	updates, err := client.ListRunnableTelegramUpdates(t.Context(), now, 10)
	if err != nil || len(updates) != 1 || updates[0].UpdateID != 10 {
		t.Fatalf("initial runnable updates = %#v, err=%v", updates, err)
	}
	claimed, ok, err := client.ClaimTelegramUpdate(t.Context(), 10, "owner-10", now, now.Add(time.Minute))
	if err != nil || !ok || claimed.AttemptCount != 1 {
		t.Fatalf("claim first: update=%#v claimed=%t err=%v", claimed, ok, err)
	}
	retryAt := now.Add(time.Minute)
	changed, err := client.ScheduleTelegramUpdateRetry(t.Context(), 10, claimed.LeaseOwner, claimed.LeaseVersion, retryAt, "sqlite", "database is busy")
	if err != nil || !changed {
		t.Fatalf("schedule retry: changed=%t err=%v", changed, err)
	}
	updates, err = client.ListRunnableTelegramUpdates(t.Context(), now.Add(30*time.Second), 10)
	if err != nil || len(updates) != 0 {
		t.Fatalf("later same-chat update bypassed retry: updates=%#v err=%v", updates, err)
	}
	updates, err = client.ListRunnableTelegramUpdates(t.Context(), retryAt, 10)
	if err != nil || len(updates) != 1 || updates[0].UpdateID != 10 {
		t.Fatalf("retry did not remain head of chat: updates=%#v err=%v", updates, err)
	}
}

func TestTelegramUpdateInboxRecoversProcessingAndDeadLettersPoison(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	inserted, err := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID:         55,
		DispatchKey:      "chat:-55",
		Payload:          []byte(`{"update_id":55}`),
		SecurityRelevant: true,
		ReceivedAt:       now,
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue update: inserted=%t err=%v", inserted, err)
	}
	_, claimed, claimErr := client.ClaimTelegramUpdate(t.Context(), 55, "owner-55", now, now.Add(time.Minute))
	if claimErr != nil || !claimed {
		t.Fatalf("claim before crash: claimed=%t err=%v", claimed, claimErr)
	}
	recovered, err := client.RecoverTelegramUpdates(t.Context(), now.Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recover processing update: recovered=%d err=%v", recovered, err)
	}
	claimedRecord, ok, err := client.ClaimTelegramUpdate(t.Context(), 55, "owner-55b", now.Add(time.Minute), now.Add(2*time.Minute))
	if err != nil || !ok || claimedRecord.AttemptCount != 2 {
		t.Fatalf("claim recovered update: update=%#v claimed=%t err=%v", claimedRecord, ok, err)
	}
	changed, err := client.DeadLetterTelegramUpdate(t.Context(), 55, claimedRecord.LeaseOwner, claimedRecord.LeaseVersion, "payload", "malformed_update", "missing update body", now.Add(2*time.Minute))
	if err != nil || !changed {
		t.Fatalf("dead letter poison update: changed=%t err=%v", changed, err)
	}
	failures, err := client.ListTelegramUpdateFailures(t.Context(), 10)
	if err != nil || len(failures) != 1 {
		t.Fatalf("list failures: failures=%#v err=%v", failures, err)
	}
	if failures[0].UpdateID != 55 || !failures[0].SecurityRelevant || failures[0].FailureReason != "malformed_update" || failures[0].AttemptCount != 2 {
		t.Fatalf("failure record = %#v", failures[0])
	}
}

func TestTelegramUpdateInboxRetentionKeepsRecentAndUnresolvedWork(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	for _, updateID := range []int{1, 2, 3} {
		inserted, enqueueErr := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
			UpdateID: updateID, DispatchKey: "chat:" + string(rune('0'+updateID)), Payload: []byte(`{"update_id":1}`), ReceivedAt: now.Add(-48 * time.Hour),
		})
		if enqueueErr != nil || !inserted {
			t.Fatalf("enqueue %d: inserted=%t err=%v", updateID, inserted, enqueueErr)
		}
	}
	for _, updateID := range []int{1, 2} {
		if _, claimed, claimErr := client.ClaimTelegramUpdate(t.Context(), updateID, fmt.Sprintf("owner-%d", updateID), now, now.Add(time.Minute)); claimErr != nil || !claimed {
			t.Fatalf("claim %d: claimed=%t err=%v", updateID, claimed, claimErr)
		}
	}
	if changed, completeErr := client.CompleteTelegramUpdate(t.Context(), 1, "owner-1", 1, "handler", now.Add(-24*time.Hour)); completeErr != nil || !changed {
		t.Fatalf("complete old update: changed=%t err=%v", changed, completeErr)
	}
	if changed, completeErr := client.CompleteTelegramUpdate(t.Context(), 2, "owner-2", 1, "handler", now); completeErr != nil || !changed {
		t.Fatalf("complete recent update: changed=%t err=%v", changed, completeErr)
	}
	deleted, err := client.CleanupTelegramUpdates(t.Context(), now.Add(-time.Hour), now.Add(-time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup updates: deleted=%d err=%v", deleted, err)
	}
	var ids []int
	if err := client.db.SelectContext(t.Context(), &ids, `SELECT update_id FROM telegram_update_inbox ORDER BY update_id`); err != nil {
		t.Fatalf("list retained updates: %v", err)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Fatalf("retained update IDs = %v, want [2 3]", ids)
	}
}
