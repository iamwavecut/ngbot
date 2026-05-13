package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestSpamCaseMessageBoundLookupAndReportMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	first, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:            -100,
		UserID:            200,
		MessageID:         40,
		MessageText:       "first spam",
		CreatedAt:         time.Now(),
		Status:            "pending",
		PreVoteRestricted: false,
	})
	if err != nil {
		t.Fatalf("create first spam case: %v", err)
	}
	second, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:            -100,
		UserID:            200,
		MessageID:         41,
		MessageText:       "second spam",
		CreatedAt:         time.Now(),
		Status:            "pending",
		PreVoteRestricted: false,
	})
	if err != nil {
		t.Fatalf("create second spam case: %v", err)
	}

	got, err := client.GetActiveSpamCaseByMessage(ctx, -100, 200, 40)
	if err != nil {
		t.Fatalf("get active spam case by message: %v", err)
	}
	if got == nil || got.ID != first.ID || got.ID == second.ID {
		t.Fatalf("expected message-bound lookup to return first case, got %#v", got)
	}

	for _, messageID := range []int{50, 51} {
		if err := client.AddSpamCaseReportMessage(ctx, &db.SpamCaseReportMessage{
			CaseID:    first.ID,
			ChatID:    -100,
			MessageID: messageID,
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("add report message %d: %v", messageID, err)
		}
	}
	if err := client.AddSpamCaseReportMessage(ctx, &db.SpamCaseReportMessage{
		CaseID:    first.ID,
		ChatID:    -100,
		MessageID: 51,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("re-add duplicate report message: %v", err)
	}

	messages, err := client.GetSpamCaseReportMessages(ctx, first.ID)
	if err != nil {
		t.Fatalf("get report messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected two report messages, got %#v", messages)
	}
	if messages[0].MessageID != 50 || messages[1].MessageID != 51 {
		t.Fatalf("expected report messages in insertion order, got %#v", messages)
	}

	if err := client.DeleteSpamCaseReportMessages(ctx, first.ID); err != nil {
		t.Fatalf("delete report messages: %v", err)
	}
	messages, err = client.GetSpamCaseReportMessages(ctx, first.ID)
	if err != nil {
		t.Fatalf("get report messages after delete: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected report messages to be deleted, got %#v", messages)
	}
}
