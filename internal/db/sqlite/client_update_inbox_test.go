package sqlite

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
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

func TestTelegramUpdateInboxAdmissionBoundsPendingRowsAndBytes(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.SetTelegramUpdateInboxLimits(db.TelegramUpdateInboxLimits{
		MaxPendingRows:          3,
		MaxPendingBytes:         12,
		MaxDispatchPendingRows:  2,
		MaxDispatchPendingBytes: 8,
		MinFreeBytes:            1,
	})

	for _, update := range []*db.TelegramUpdate{
		{UpdateID: 1, DispatchKey: "chat:1", Payload: []byte("1234"), ReceivedAt: time.Now()},
		{UpdateID: 2, DispatchKey: "chat:1", Payload: []byte("5678"), ReceivedAt: time.Now()},
	} {
		inserted, enqueueErr := client.EnqueueTelegramUpdate(t.Context(), update)
		if enqueueErr != nil || !inserted {
			t.Fatalf("enqueue %d: inserted=%t err=%v", update.UpdateID, inserted, enqueueErr)
		}
	}

	inserted, err := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: 3, DispatchKey: "chat:1", Payload: []byte("security"), SecurityRelevant: true, ReceivedAt: time.Now(),
	})
	if inserted || !errors.Is(err, db.ErrTelegramUpdateInboxCapacity) {
		t.Fatalf("per-key overload = inserted=%t err=%v, want capacity backpressure", inserted, err)
	}
	inserted, err = client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: 3, DispatchKey: "chat:2", Payload: []byte("1234"), ReceivedAt: time.Now(),
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue third global row: inserted=%t err=%v", inserted, err)
	}
	inserted, err = client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: 4, DispatchKey: "chat:3", Payload: []byte("1"), SecurityRelevant: true, ReceivedAt: time.Now(),
	})
	if inserted || !errors.Is(err, db.ErrTelegramUpdateInboxCapacity) {
		t.Fatalf("global overload = inserted=%t err=%v, want capacity backpressure", inserted, err)
	}

	var rows int
	var bytes int64
	if err := client.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*), COALESCE(SUM(payload_bytes), 0)
		FROM telegram_update_inbox
		WHERE status IN ('pending', 'retry')
	`).Scan(&rows, &bytes); err != nil {
		t.Fatalf("read bounded inbox usage: %v", err)
	}
	if rows != 3 || bytes != 12 {
		t.Fatalf("bounded inbox usage = rows=%d bytes=%d, want rows=3 bytes=12", rows, bytes)
	}
}

func TestTelegramUpdateInboxAdmissionChecksFreeSpaceAndKeepsDuplicateIdempotent(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.SetTelegramUpdateInboxLimits(db.TelegramUpdateInboxLimits{
		MaxPendingRows: 10, MaxPendingBytes: 1 << 20,
		MaxDispatchPendingRows: 10, MaxDispatchPendingBytes: 1 << 20,
		MinFreeBytes: 1024,
	})
	update := &db.TelegramUpdate{UpdateID: 10, DispatchKey: "chat:10", Payload: []byte("payload"), SecurityRelevant: true, ReceivedAt: time.Now()}
	inserted, err := client.EnqueueTelegramUpdate(t.Context(), update)
	if err != nil || !inserted {
		t.Fatalf("seed update: inserted=%t err=%v", inserted, err)
	}
	client.databaseFreeBytes = func() (uint64, error) { return 0, nil }
	inserted, err = client.EnqueueTelegramUpdate(t.Context(), update)
	if err != nil || inserted {
		t.Fatalf("duplicate under free-space pressure: inserted=%t err=%v", inserted, err)
	}
	inserted, err = client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: 11, DispatchKey: "chat:11", Payload: []byte("payload"), SecurityRelevant: true, ReceivedAt: time.Now(),
	})
	if inserted || !errors.Is(err, db.ErrTelegramUpdateInboxCapacity) {
		t.Fatalf("free-space overload = inserted=%t err=%v, want capacity backpressure", inserted, err)
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

func TestTelegramUpdateInboxRejectsLateLeaseRenewal(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now()
	if inserted, enqueueErr := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: 11, DispatchKey: "chat:-11", Payload: []byte(`{"update_id":11}`), ReceivedAt: now,
	}); enqueueErr != nil || !inserted {
		t.Fatalf("enqueue update: inserted=%t err=%v", inserted, enqueueErr)
	}
	leaseUntil := now.Add(time.Second)
	claimed, ok, err := client.ClaimTelegramUpdate(t.Context(), 11, "owner", now, leaseUntil)
	if err != nil || !ok {
		t.Fatalf("claim update: update=%#v ok=%t err=%v", claimed, ok, err)
	}
	renewed, err := client.RenewTelegramUpdateLease(t.Context(), 11, claimed.LeaseOwner, claimed.LeaseVersion, leaseUntil.Add(time.Second), leaseUntil.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("renew expired lease: %v", err)
	}
	if renewed {
		t.Fatal("expired lease was resurrected")
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

func TestTelegramUpdateInboxSanitizesDurableFailuresAndReleasesTerminalPayload(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{"update_id":77,"message":{"text":"private Telegram payload"}}`)
	inserted, err := client.EnqueueTelegramUpdate(t.Context(), &db.TelegramUpdate{
		UpdateID: 77, DispatchKey: "chat:77", Payload: payload, SecurityRelevant: true, ReceivedAt: now,
	})
	if err != nil || !inserted {
		t.Fatalf("enqueue update: inserted=%t err=%v", inserted, err)
	}
	claimed, ok, err := client.ClaimTelegramUpdate(t.Context(), 77, "owner", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim update: claimed=%t err=%v", ok, err)
	}
	const sensitive = `POST https://api.telegram.org/bot123456:SECRET/sendMessage body={"text":"private"}`
	changed, err := client.DeadLetterTelegramUpdate(
		t.Context(), 77, claimed.LeaseOwner, claimed.LeaseVersion,
		"attacker-controlled-source", "attacker controlled reason", sensitive, now.Add(time.Minute),
	)
	if err != nil || !changed {
		t.Fatalf("dead letter update: changed=%t err=%v", changed, err)
	}

	var inboxPayload []byte
	var payloadBytes int64
	var inboxLastError, inboxDigest, outcomeSource string
	if err := client.db.QueryRowContext(t.Context(), `
		SELECT payload, payload_bytes, last_error, error_digest, outcome_source
		FROM telegram_update_inbox WHERE update_id = 77
	`).Scan(&inboxPayload, &payloadBytes, &inboxLastError, &inboxDigest, &outcomeSource); err != nil {
		t.Fatalf("read terminal inbox row: %v", err)
	}
	const wantDigest = "sha256:9a69ff577525a4f4f71d065a19349585faea85ec709f7f82bd6cd28113d7bb2c"
	if len(inboxPayload) != 0 || payloadBytes != 0 || inboxLastError != "" || inboxDigest != wantDigest || outcomeSource != "runtime" {
		t.Fatalf("terminal inbox row retained unsafe data: payload=%q bytes=%d last_error=%q digest=%q source=%q", inboxPayload, payloadBytes, inboxLastError, inboxDigest, outcomeSource)
	}

	var failureSource, failureReason, failureLastError, failureDigest string
	if err := client.db.QueryRowContext(t.Context(), `
		SELECT failure_source, failure_reason, last_error, error_digest
		FROM telegram_update_failures WHERE update_id = 77
	`).Scan(&failureSource, &failureReason, &failureLastError, &failureDigest); err != nil {
		t.Fatalf("read durable failure: %v", err)
	}
	if failureSource != "runtime" || failureReason != "unclassified_error" || failureLastError != "" || failureDigest != wantDigest {
		t.Fatalf("failure taxonomy = source=%q reason=%q last_error=%q digest=%q", failureSource, failureReason, failureLastError, failureDigest)
	}
	if strings.Contains(inboxDigest+failureDigest, "SECRET") {
		t.Fatal("durable failure digest leaked sensitive input")
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

func TestTelegramUpdateInboxCleanupDrainsBacklogInBatchesAndProtectsActiveRows(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	tx, err := client.db.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	for updateID := 1; updateID <= 1_205; updateID++ {
		if _, err := tx.ExecContext(t.Context(), `
			INSERT INTO telegram_update_inbox (
				update_id, dispatch_key, payload, payload_bytes, security_relevant,
				status, available_at, received_at, completed_at
			) VALUES (?, ?, x'', 0, 0, 'completed', ?, ?, ?)
		`, updateID, fmt.Sprintf("chat:%d", updateID), now, now, now.Add(-48*time.Hour)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed completed update %d: %v", updateID, err)
		}
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO telegram_update_inbox (
			update_id, dispatch_key, payload, payload_bytes, security_relevant,
			status, available_at, received_at
		) VALUES (2000, 'chat:active', x'01', 1, 1, 'retry', ?, ?)
	`, now, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed active update: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed transaction: %v", err)
	}

	deleted, err := client.CleanupTelegramUpdates(t.Context(), now.Add(-time.Hour), now.Add(-time.Hour))
	if err != nil || deleted != 1_205 {
		t.Fatalf("cleanup backlog: deleted=%d err=%v", deleted, err)
	}
	var remaining []int
	if err := client.db.SelectContext(t.Context(), &remaining, `SELECT update_id FROM telegram_update_inbox ORDER BY update_id`); err != nil {
		t.Fatalf("list remaining rows: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != 2000 {
		t.Fatalf("remaining update IDs = %v, want active update 2000", remaining)
	}
}
