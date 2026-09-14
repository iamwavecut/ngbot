package sqlite

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestMessageContextStoresLatestEditWithoutExtendingOriginalAge(t *testing.T) {
	const newestEdit = "newest edit"
	t.Parallel()

	client := newAuthorTrustClient(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	original := db.MessageContext{ChatID: -100, MessageID: 1, ThreadID: 10, ReplyToMessageID: 20, AuthorKind: db.MessageAuthorUser, AuthorID: 300, Text: strings.Repeat("界", 2100), SentAt: now, UpdatedAt: now}
	if err := client.UpsertMessageContext(t.Context(), &original); err != nil {
		t.Fatalf("store original message: %v", err)
	}
	stored, err := client.MessageContext(t.Context(), -100, 1)
	if err != nil || stored == nil || utf8.RuneCountInString(stored.Text) != 2000 || !utf8.ValidString(stored.Text) {
		t.Fatalf("bounded context=%+v err=%v", stored, err)
	}
	if utf8.RuneCountInString(original.Text) != 2100 {
		t.Fatal("upsert mutated caller message")
	}
	for _, test := range []struct {
		text   string
		delta  time.Duration
		wanted string
	}{
		{text: newestEdit, delta: 2 * time.Hour, wanted: newestEdit},
		{text: "stale edit", delta: time.Hour, wanted: newestEdit},
		{text: "replayed original", wanted: newestEdit},
		{text: "same version", delta: 2 * time.Hour, wanted: newestEdit},
		{delta: 3 * time.Hour},
	} {
		edit := original
		edit.Text = test.text
		edit.SentAt = now.Add(test.delta)
		edit.UpdatedAt = now.Add(test.delta)
		if err := client.UpsertMessageContext(t.Context(), &edit); err != nil {
			t.Fatalf("upsert edit: %v", err)
		}
		stored, err := client.MessageContext(t.Context(), -100, 1)
		if err != nil || stored == nil || stored.Text != test.wanted || !stored.SentAt.Equal(now) || stored.ThreadID != 10 || stored.ReplyToMessageID != 20 {
			t.Fatalf("latest context=%+v wantText=%q err=%v", stored, test.wanted, err)
		}
	}
}

func TestRecentMessageContextIsolatesChatThreadAgeAndCandidate(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-200)); err != nil {
		t.Fatalf("create other chat: %v", err)
	}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		chatID    int64
		messageID int
		threadID  int
		age       time.Duration
		text      string
	}{
		{chatID: -100, messageID: 1, threadID: 10, age: 25 * time.Hour, text: "too old"},
		{chatID: -100, messageID: 2, threadID: 10, age: 24 * time.Hour, text: "cutoff"},
		{chatID: -100, messageID: 3, threadID: 10, age: time.Hour, text: "recent"},
		{chatID: -100, messageID: 4, threadID: 10, text: "recent"},
		{chatID: -100, messageID: 5, threadID: 10},
		{chatID: -100, messageID: 6, threadID: 20, text: "other thread"},
		{chatID: -200, messageID: 7, threadID: 10, text: "other chat"},
		{chatID: -100, messageID: 8, threadID: 0, text: "main chat"},
		{chatID: -100, messageID: 9, threadID: 10, text: "candidate"},
		{chatID: -100, messageID: 10, threadID: 10, text: "future message"},
	} {
		record := db.MessageContext{ChatID: test.chatID, MessageID: test.messageID, ThreadID: test.threadID, AuthorKind: db.MessageAuthorSenderChat, AuthorID: -300, Text: test.text, SentAt: now.Add(-test.age), UpdatedAt: now}
		if err := client.UpsertMessageContext(t.Context(), &record); err != nil {
			t.Fatalf("store message %d: %v", test.messageID, err)
		}
	}
	for _, test := range []struct {
		limit int
		want  []int
	}{
		{limit: 2, want: []int{4, 3}},
		{limit: 10, want: []int{4, 3, 2}},
	} {
		messages, err := client.RecentMessageContext(t.Context(), -100, 10, 9, now.Add(-24*time.Hour), test.limit)
		if err != nil {
			t.Fatalf("query recent context: %v", err)
		}
		ids := make([]int, 0, len(messages))
		for _, message := range messages {
			ids = append(ids, message.MessageID)
		}
		if !slices.Equal(ids, test.want) {
			t.Fatalf("context IDs=%v, want %v", ids, test.want)
		}
	}
}

func TestDeletedMessageContextCannotBeResurrectedByReplayOrAfterRestart(t *testing.T) {
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
	now := time.Now().UTC()
	message := db.MessageContext{ChatID: -100, MessageID: 1, AuthorKind: db.MessageAuthorUser, AuthorID: 300, Text: "removed", SentAt: now, UpdatedAt: now}
	if err := client.UpsertMessageContext(t.Context(), &message); err != nil {
		t.Fatalf("store message: %v", err)
	}
	for _, messageID := range []int{1, 2} {
		if err := client.DeleteMessageContext(t.Context(), -100, messageID); err != nil {
			t.Fatalf("delete context %d: %v", messageID, err)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	client, err = NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	for _, messageID := range []int{1, 2} {
		message.MessageID = messageID
		message.UpdatedAt = now.Add(time.Minute)
		if err := client.UpsertMessageContext(t.Context(), &message); err != nil {
			t.Fatalf("replay removed message: %v", err)
		}
		stored, err := client.MessageContext(t.Context(), -100, messageID)
		if err != nil || stored != nil {
			t.Fatalf("deleted context resurrected=%+v err=%v", stored, err)
		}
	}
}

func TestMessageContextRetentionUsesOriginalSentAtAndDrainsBoundedBatches(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for messageID, age := range []time.Duration{25 * time.Hour, 26 * time.Hour, 27 * time.Hour, 23 * time.Hour} {
		message := db.MessageContext{ChatID: -100, MessageID: messageID + 1, AuthorKind: db.MessageAuthorUser, AuthorID: 300, Text: "recently edited", SentAt: now.Add(-age), UpdatedAt: now}
		if err := client.UpsertMessageContext(t.Context(), &message); err != nil {
			t.Fatalf("store context: %v", err)
		}
	}
	if _, err := client.db.ExecContext(t.Context(), `INSERT INTO chat_message_context_tombstones (chat_id, message_id, deleted_at) VALUES (-100, 10, ?), (-100, 11, ?), (-100, 12, ?)`, now.Add(-25*time.Hour), now.Add(-26*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed tombstones: %v", err)
	}
	result, err := client.CleanupRetention(t.Context(), now, 1)
	if err != nil {
		t.Fatalf("bounded cleanup: %v", err)
	}
	if result.MessageContexts != 1 || result.MessageContextTombstones != 1 {
		t.Fatalf("cleanup exceeded or missed per-table limit: %+v", result)
	}
	if err := client.CleanupRetainedRecords(t.Context(), now, 1); err != nil {
		t.Fatalf("drain context batches: %v", err)
	}
	assertIDs(t, client, "chat_message_context", "message_id", []int64{4})
	assertIDs(t, client, "chat_message_context_tombstones", "message_id", []int64{12})
}

func TestCheckedBindingsSurviveRetentionRegardlessOfAssociatedCaseKind(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if _, err := client.db.ExecContext(t.Context(), `
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, author_kind, challenged_at)
		VALUES (-100, 1, -300, 'sender_chat', ?), (-100, 2, -300, 'sender_chat', ?);
		INSERT INTO spam_cases (chat_id, user_id, author_kind, message_id, message_text, created_at, status)
		VALUES (-100, -300, 'user', 1, 'legacy', ?, 'pending'), (-100, -300, 'sender_chat', 2, 'active', ?, 'pending')
	`, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour), now, now); err != nil {
		t.Fatalf("seed typed bindings: %v", err)
	}
	if _, err := client.CleanupRetention(t.Context(), now, 10); err != nil {
		t.Fatalf("cleanup bindings: %v", err)
	}
	assertIDs(t, client, testTableChallengedMessages, "message_id", []int64{1, 2})
}
