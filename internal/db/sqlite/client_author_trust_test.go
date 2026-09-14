package sqlite

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestAuthorTrustCountsOnlyDistinctEligibleMessagesAndRenewsAfterExpiry(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		messageID int
		eligible  bool
		wantCount int
		wantNew   bool
	}{
		{messageID: 1, wantNew: true},
		{messageID: 1, eligible: true},
		{messageID: 2, eligible: true, wantCount: 1, wantNew: true},
		{messageID: 3, eligible: true, wantCount: 2, wantNew: true},
		{messageID: 4, eligible: true, wantCount: 3, wantNew: true},
		{messageID: 5, eligible: true, wantCount: 3, wantNew: true},
	} {
		trust, inserted, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, test.messageID, now, 3, 30*24*time.Hour, test.eligible)
		if err != nil {
			t.Fatalf("record %d: %v", test.messageID, err)
		}
		if inserted != test.wantNew || trust.SafeMessages != test.wantCount || trust.Trusted(now) != (test.wantCount == 3) {
			t.Fatalf("message %d: trust=%+v inserted=%t", test.messageID, trust, inserted)
		}
	}
	trust, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 8, now.Add(15*24*time.Hour), 3, 30*24*time.Hour, true)
	if err != nil || trust == nil || !trust.TrustedUntil.Time.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("active trust expiry was extended: trust=%+v err=%v", trust, err)
	}
	expiredAt := now.Add(30 * 24 * time.Hour)
	for _, test := range []struct {
		messageID int
		eligible  bool
		trusted   bool
	}{
		{messageID: 5, eligible: true},
		{messageID: 6},
		{messageID: 7, eligible: true, trusted: true},
	} {
		trust, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, test.messageID, expiredAt, 3, 30*24*time.Hour, test.eligible)
		if err != nil || trust == nil || trust.SafeMessages != 3 || trust.Trusted(expiredAt) != test.trusted {
			t.Fatalf("renewal %d: trust=%+v err=%v", test.messageID, trust, err)
		}
		if test.trusted && !trust.TrustedUntil.Time.Equal(now.Add(60*24*time.Hour)) {
			t.Fatalf("renewal expiry = %v", trust.TrustedUntil)
		}
	}
}

func TestAuthorTrustAndCheckedBindingsSurviveRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	client, err := NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 300}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true); err != nil {
		t.Fatalf("record safe message: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	client, err = NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	trust, err := client.EnsureMessageTrust(t.Context(), -100, author)
	if err != nil || trust == nil || !trust.Trusted(now) || !trust.TrustedUntil.Time.Equal(now.Add(time.Hour)) {
		t.Fatalf("reopened trust = %+v, err=%v", trust, err)
	}
	checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1)
	if err != nil || !checked {
		t.Fatalf("reopened checked message = %t, err=%v", checked, err)
	}
	if _, err := client.db.ExecContext(t.Context(), `DELETE FROM chats WHERE id = -100`); err != nil {
		t.Fatalf("delete parent chat: %v", err)
	}
	trust, err = client.MessageTrust(t.Context(), -100, author)
	if err != nil || trust != nil {
		t.Fatalf("cascaded trust = %+v, err=%v", trust, err)
	}
}

func TestAuthorTrustSerializesDuplicateAndDistinctConcurrentMessages(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 300}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	var insertedCount atomic.Int32
	var group sync.WaitGroup
	for n := range 32 {
		group.Go(func() {
			_, inserted, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, n%8+1, now, 3, time.Hour, true)
			if err != nil {
				t.Errorf("concurrent safe message: %v", err)
			}
			if inserted {
				insertedCount.Add(1)
			}
		})
	}
	group.Wait()
	trust, err := client.MessageTrust(t.Context(), -100, author)
	if err != nil || trust == nil || trust.SafeMessages != 3 || !trust.Trusted(now) || insertedCount.Load() != 8 {
		t.Fatalf("concurrent result trust=%+v inserted=%d err=%v", trust, insertedCount.Load(), err)
	}
}

func TestAuthorTrustTransactionRollsBackBothTrustAndBinding(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	if _, err := client.db.ExecContext(t.Context(), `
		CREATE TRIGGER reject_trust_update BEFORE UPDATE ON chat_author_trust
		BEGIN SELECT RAISE(ABORT, 'trust write failed'); END
	`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 300}
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, time.Now(), 3, time.Hour, true); err == nil {
		t.Fatal("failed trust write was accepted")
	}
	trust, err := client.MessageTrust(t.Context(), -100, author)
	if err != nil || trust != nil {
		t.Fatalf("failed transaction retained trust=%+v err=%v", trust, err)
	}
	checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1)
	if err != nil || checked {
		t.Fatalf("failed transaction retained checked=%t err=%v", checked, err)
	}
}

func TestAuthorTrustSuspendsWithoutLosingExpiryAndResetRetainsEditProtection(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"pending", "resolving_spam", "resolving_false_positive"} {
		t.Run(status, func(t *testing.T) {
			client := newAuthorTrustClient(t)
			author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
			now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
			original, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true)
			if err != nil {
				t.Fatalf("grant trust: %v", err)
			}
			if _, err := client.db.ExecContext(t.Context(), `INSERT INTO spam_cases (chat_id, author_kind, user_id, message_id, message_text, created_at, status) VALUES (-100, 'sender_chat', -300, 10, 'case', ?, ?)`, now, status); err != nil {
				t.Fatalf("create suspending case: %v", err)
			}
			trust, err := client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || !trust.Suspended || trust.Trusted(now) {
				t.Fatalf("active case did not suspend trust=%+v err=%v", trust, err)
			}
			trust, inserted, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 2, now.Add(time.Hour), 1, time.Hour, true)
			if err != nil || trust == nil || !inserted || !trust.Suspended || trust.SafeMessages != original.SafeMessages || trust.TrustedUntil != original.TrustedUntil {
				t.Fatalf("suspended write changed trust=%+v inserted=%t err=%v", trust, inserted, err)
			}
			if _, err := client.db.ExecContext(t.Context(), `UPDATE spam_cases SET status = 'false_positive'`); err != nil {
				t.Fatalf("resolve false positive: %v", err)
			}
			trust, err = client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || !trust.Trusted(now) || trust.TrustedUntil != original.TrustedUntil {
				t.Fatalf("false positive lost remaining trust=%+v err=%v", trust, err)
			}
			if err := client.ResetMessageTrust(t.Context(), -100, author); err != nil {
				t.Fatalf("reset trust: %v", err)
			}
			trust, inserted, err = client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true)
			if err != nil || trust == nil || inserted || trust.SafeMessages != 0 || trust.TrustedUntil.Valid {
				t.Fatalf("replayed binding restored reset trust=%+v inserted=%t err=%v", trust, inserted, err)
			}
			checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1)
			if err != nil || !checked {
				t.Fatalf("reset lost edit protection=%t err=%v", checked, err)
			}
		})
	}
}

func TestAuthorTrustDoesNotReinterpretLegacyKindsOrReuseAnotherAuthorBinding(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	if _, err := client.db.ExecContext(t.Context(), `
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, author_kind)
		VALUES (-100, 1, -300, 'user'), (-100, 2, 300, 'unknown'), (-100, 3, -300, 'sender_chat');
		INSERT INTO spam_cases (chat_id, author_kind, user_id, message_id, message_text, created_at, status)
		VALUES (-100, 'user', -300, 10, 'legacy', CURRENT_TIMESTAMP, 'pending')
	`); err != nil {
		t.Fatalf("seed legacy identities: %v", err)
	}
	author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
	trust, inserted, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, time.Now(), 1, time.Hour, true)
	if err != nil || trust == nil || inserted || trust.Suspended || trust.SafeMessages != 0 {
		t.Fatalf("reinterpreted legacy author: trust=%+v inserted=%t err=%v", trust, inserted, err)
	}
	checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1)
	if err != nil || checked {
		t.Fatalf("legacy binding matched sender chat: checked=%t err=%v", checked, err)
	}
	for _, test := range []struct {
		userID    int64
		messageID int
	}{
		{userID: 300, messageID: 2},
		{userID: -300, messageID: 3},
	} {
		checked, err := client.IsChallengedMessage(t.Context(), -100, test.userID, test.messageID)
		if err != nil || checked {
			t.Fatalf("legacy user API matched another kind: checked=%t err=%v", checked, err)
		}
	}
}

func TestAuthorTrustRejectsInvalidAuthorsWithoutPersisting(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	for _, author := range []db.MessageAuthor{{}, {Kind: "unknown", ID: 300}, {Kind: "user", ID: -300}, {Kind: "sender_chat", ID: 300}} {
		if _, err := client.EnsureMessageTrust(t.Context(), -100, author); err == nil {
			t.Errorf("EnsureMessageTrust accepted %+v", author)
		}
		if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, time.Now(), 3, time.Hour, true); err == nil {
			t.Errorf("RecordSafeAuthorMessage accepted %+v", author)
		}
	}
	var count int
	if err := client.db.GetContext(t.Context(), &count, `SELECT COUNT(*) FROM chat_author_trust`); err != nil || count != 0 {
		t.Fatalf("invalid author persisted rows=%d err=%v", count, err)
	}
}

func TestPendingCasePreventsTrustCounterAdvancement(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 300}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 3, time.Hour, true); err != nil {
		t.Fatalf("start trust: %v", err)
	}
	if _, err := client.db.ExecContext(t.Context(), `INSERT INTO spam_cases (chat_id, user_id, message_id, message_text, created_at, status) VALUES (-100, 300, 10, 'pending', ?, 'pending')`, now); err != nil {
		t.Fatalf("create pending case: %v", err)
	}
	trust, inserted, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 2, now, 3, time.Hour, true)
	if err != nil || trust == nil || !inserted || !trust.Suspended || trust.SafeMessages != 1 || trust.TrustedUntil.Valid {
		t.Fatalf("pending case advanced trust: trust=%+v inserted=%t err=%v", trust, inserted, err)
	}
}

func newAuthorTrustClient(t *testing.T) *sqliteClient {
	t.Helper()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	return client
}
