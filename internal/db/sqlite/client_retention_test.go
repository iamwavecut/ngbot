package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestCleanupRetentionHonorsCutoffsReferencesAndBatchLimit(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	challengedCutoff := now.Add(-30 * 24 * time.Hour)
	recentJoinerCutoff := now.Add(-processedRecentJoinerRetention)
	spamCaseCutoff := now.Add(-terminalSpamCaseRetention)
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, challenged_at)
		VALUES
			(-100, 1, 11, ?),
			(-100, 2, 12, ?),
			(-100, 3, 13, ?),
			(-100, 4, 14, ?)
	`, challengedCutoff.Add(-time.Second), challengedCutoff.Add(-2*time.Second), challengedCutoff, challengedCutoff.Add(time.Second)); err != nil {
		t.Fatalf("seed challenged messages: %v", err)
	}
	if _, err := client.db.ExecContext(
		ctx, `
		INSERT INTO spam_cases (
			id, chat_id, user_id, message_id, message_text, created_at,
			pre_vote_restricted, status, resolved_at
		) VALUES
			(101, -100, 11, 1, 'active reference', ?, 1, 'pending', NULL),
			(102, -100, 21, 21, 'old terminal', ?, 1, 'spam', ?),
			(103, -100, 22, 22, 'cutoff terminal', ?, 1, 'false_positive', ?),
			(104, -100, 23, 23, 'new terminal', ?, 1, 'not_enforced', ?),
			(105, -100, 24, 24, 'resolving', ?, 1, 'resolving_spam', ?),
			(106, -100, 25, 25, 'audit reference', ?, 1, 'spam', ?)
	`,
		now.Add(-time.Hour),
		spamCaseCutoff.Add(-time.Second), spamCaseCutoff.Add(-time.Second),
		spamCaseCutoff, spamCaseCutoff,
		spamCaseCutoff.Add(time.Second), spamCaseCutoff.Add(time.Second),
		spamCaseCutoff.Add(-time.Hour), spamCaseCutoff.Add(-time.Hour),
		spamCaseCutoff.Add(-time.Hour), spamCaseCutoff.Add(-time.Hour),
	); err != nil {
		t.Fatalf("seed spam cases: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO spam_case_report_messages (case_id, chat_id, message_id, created_at)
		VALUES (106, -100, 600, ?)
	`, now); err != nil {
		t.Fatalf("seed retained report reference: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO recent_joiners (
			id, join_message_id, chat_id, user_id, username, joined_at, processed, is_spammer
		) VALUES
			(201, 1, -100, 31, 'old', ?, 1, 0),
			(202, 2, -100, 32, 'cutoff', ?, 1, 0),
			(203, 3, -100, 33, 'new', ?, 1, 0),
			(204, 4, -100, 34, 'pending', ?, 0, 0)
	`, recentJoinerCutoff.Add(-time.Second), recentJoinerCutoff, recentJoinerCutoff.Add(time.Second), recentJoinerCutoff.Add(-time.Hour)); err != nil {
		t.Fatalf("seed recent joiners: %v", err)
	}

	first, err := client.CleanupRetention(ctx, now, 1)
	if err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if first.ProcessedRecentJoiners != 1 || first.TerminalSpamCases != 1 {
		t.Fatalf("first cleanup exceeded or missed per-table limit: %+v", first)
	}
	second, err := client.CleanupRetention(ctx, now, 10)
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if second.ProcessedRecentJoiners != 1 || second.TerminalSpamCases != 1 {
		t.Fatalf("second cleanup did not honor inclusive cutoff: %+v", second)
	}

	assertIDs(t, client, "chat_challenged_messages", "message_id", []int64{1, 2, 3, 4})
	assertIDs(t, client, "recent_joiners", "id", []int64{203, 204})
	assertIDs(t, client, "spam_cases", "id", []int64{101, 104, 105, 106})
}

func TestCleanupRetentionPreservesBindingsBeyondCaseRetention(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	challengedAt := now.Add(-31 * 24 * time.Hour)
	auditCutoff := now.Add(-terminalSpamCaseRetention)
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, challenged_at)
		VALUES
			(-100, 1, 11, ?),
			(-100, 2, 12, ?),
			(-100, 3, 13, ?),
			(-100, 4, 14, ?),
			(-100, 5, 15, ?),
			(-100, 6, 16, ?)
	`, challengedAt, challengedAt, challengedAt, challengedAt, challengedAt, challengedAt); err != nil {
		t.Fatalf("seed challenged messages: %v", err)
	}
	if _, err := client.db.ExecContext(
		ctx, `
		INSERT INTO spam_cases (
			id, chat_id, user_id, message_id, message_text, created_at,
			pre_vote_restricted, status, resolved_at
		) VALUES
			(102, -100, 12, 2, 'pending reference', ?, 1, 'pending', NULL),
			(103, -100, 13, 3, 'inside audit window', ?, 1, 'spam', ?),
			(104, -100, 14, 4, 'at audit cutoff', ?, 1, 'false_positive', ?),
			(105, -100, 15, 5, 'queued deletion artifact', ?, 1, 'not_enforced', ?),
			(106, -100, 16, 6, 'expired terminal case', ?, 1, 'spam', ?)
	`,
		now.Add(-time.Hour),
		auditCutoff.Add(time.Second), auditCutoff.Add(time.Second),
		auditCutoff, auditCutoff,
		auditCutoff.Add(-time.Second), auditCutoff.Add(-time.Second),
		auditCutoff.Add(-time.Second), auditCutoff.Add(-time.Second),
	); err != nil {
		t.Fatalf("seed spam cases: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO spam_case_report_messages (case_id, chat_id, message_id, created_at)
		VALUES (105, -100, 500, ?)
	`, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed queued report deletion: %v", err)
	}

	result, err := client.CleanupRetention(ctx, now, 100)
	if err != nil {
		t.Fatalf("cleanup retention: %v", err)
	}
	if result.TerminalSpamCases != 2 {
		t.Fatalf("cleanup result = %+v, want 2 terminal cases and preserved checked bindings", result)
	}
	assertIDs(t, client, "chat_challenged_messages", "message_id", []int64{1, 2, 3, 4, 5, 6})
	assertIDs(t, client, "spam_cases", "id", []int64{102, 103, 105})
}

func TestRetentionCleanupRunsAfterCrashRestart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	stale := time.Now().UTC().Add(-terminalSpamCaseRetention - 24*time.Hour)
	if _, err := client.db.ExecContext(ctx, `
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, challenged_at)
		VALUES (-100, 1, 11, ?);
		INSERT INTO recent_joiners (
			join_message_id, chat_id, user_id, username, joined_at, processed, is_spammer
		) VALUES (1, -100, 21, 'old', ?, 1, 0);
		INSERT INTO spam_cases (
			chat_id, user_id, message_id, message_text, created_at,
			pre_vote_restricted, status, resolved_at
		) VALUES (-100, 31, 3, 'old', ?, 1, 'spam', ?)
	`, stale, stale, stale, stale); err != nil {
		t.Fatalf("seed stale rows: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("simulate process crash close: %v", err)
	}

	reopened, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.CleanupRetainedRecords(ctx, time.Now().UTC(), retentionCleanupBatchSize); err != nil {
		t.Fatalf("run startup retention cleanup: %v", err)
	}
	assertIDs(t, reopened, "chat_challenged_messages", "message_id", []int64{1})
	for _, table := range []string{"recent_joiners", "spam_cases"} {
		var count int
		if err := reopened.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+table); err != nil {
			t.Fatalf("count %s after restart: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("stale %s survived restart cleanup: %d", table, count)
		}
	}
}

func TestCleanupRetainedRecordsDrainsMultipleBoundedBatches(t *testing.T) {
	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		WITH RECURSIVE sequence(id) AS (
			SELECT 1
			UNION ALL
			SELECT id + 1 FROM sequence WHERE id < 1201
		)
		INSERT INTO recent_joiners (
			join_message_id, chat_id, user_id, username, joined_at, processed, is_spammer
		)
		SELECT id, -100, 10000 + id, 'legacy', ?, 1, 0 FROM sequence
	`, now.Add(-processedRecentJoinerRetention-time.Hour)); err != nil {
		t.Fatalf("seed legacy recent joiners: %v", err)
	}

	if err := client.CleanupRetainedRecords(ctx, now, retentionCleanupBatchSize); err != nil {
		t.Fatalf("drain retained records: %v", err)
	}
	assertIDs(t, client, "recent_joiners", "id", nil)
}

func TestCleanupRetainedRecordsObservesCancellationBetweenBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if _, err := client.db.ExecContext(ctx, `
		WITH RECURSIVE sequence(id) AS (
			SELECT 1
			UNION ALL
			SELECT id + 1 FROM sequence WHERE id < 501
		)
		INSERT INTO recent_joiners (
			join_message_id, chat_id, user_id, username, joined_at, processed, is_spammer
		)
		SELECT id, -100, 20000 + id, 'legacy', ?, 1, 0 FROM sequence
	`, now.Add(-processedRecentJoinerRetention-time.Hour)); err != nil {
		t.Fatalf("seed legacy recent joiners: %v", err)
	}
	client.retentionCleanupBetweenBatches = cancel

	err = client.CleanupRetainedRecords(ctx, now, retentionCleanupBatchSize)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup error = %v, want context cancellation", err)
	}
	var remaining int
	if err := client.db.GetContext(t.Context(), &remaining, `SELECT COUNT(*) FROM recent_joiners`); err != nil {
		t.Fatalf("count retained records: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining records = %d, want one record after a bounded 500-row batch", remaining)
	}
}

func assertIDs(t *testing.T, client *sqliteClient, table, column string, want []int64) {
	t.Helper()
	var got []int64
	if err := client.db.SelectContext(t.Context(), &got, `SELECT `+column+` FROM `+table+` ORDER BY `+column); err != nil {
		t.Fatalf("read retained %s IDs: %v", table, err)
	}
	if len(got) != len(want) {
		t.Fatalf("retained %s IDs = %v, want %v", table, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("retained %s IDs = %v, want %v", table, got, want)
		}
	}
}
