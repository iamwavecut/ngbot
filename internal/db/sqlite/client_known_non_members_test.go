package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestChatKnownNonMemberUpsertLookupDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestSQLiteClient(t)

	record := &db.ChatKnownNonMember{
		ChatID:    -1001234567890,
		UserID:    200,
		CreatedAt: time.Now().Add(-time.Minute),
	}
	if err := client.UpsertChatKnownNonMember(ctx, record); err != nil {
		t.Fatalf("UpsertChatKnownNonMember: %v", err)
	}

	matched, err := client.IsChatKnownNonMember(ctx, record.ChatID, record.UserID)
	if err != nil {
		t.Fatalf("IsChatKnownNonMember: %v", err)
	}
	if !matched {
		t.Fatal("expected known non-member lookup to match")
	}

	stored, err := client.getChatKnownNonMember(ctx, record.ChatID, record.UserID)
	if err != nil {
		t.Fatalf("getChatKnownNonMember: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored known non-member record")
	}
	firstCreatedAt := stored.CreatedAt
	firstUpdatedAt := stored.UpdatedAt

	time.Sleep(5 * time.Millisecond)

	if err := client.UpsertChatKnownNonMember(ctx, &db.ChatKnownNonMember{
		ChatID: record.ChatID,
		UserID: record.UserID,
	}); err != nil {
		t.Fatalf("UpsertChatKnownNonMember duplicate: %v", err)
	}

	stored, err = client.getChatKnownNonMember(ctx, record.ChatID, record.UserID)
	if err != nil {
		t.Fatalf("getChatKnownNonMember after duplicate upsert: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored known non-member record after duplicate upsert")
	}
	if !stored.CreatedAt.Equal(firstCreatedAt) {
		t.Fatalf("expected created_at to stay stable, got %s want %s", stored.CreatedAt, firstCreatedAt)
	}
	if !stored.UpdatedAt.After(firstUpdatedAt) {
		t.Fatalf("expected updated_at to move forward, got %s want after %s", stored.UpdatedAt, firstUpdatedAt)
	}

	if err := client.DeleteChatKnownNonMember(ctx, record.ChatID, record.UserID); err != nil {
		t.Fatalf("DeleteChatKnownNonMember: %v", err)
	}

	matched, err = client.IsChatKnownNonMember(ctx, record.ChatID, record.UserID)
	if err != nil {
		t.Fatalf("IsChatKnownNonMember after delete: %v", err)
	}
	if matched {
		t.Fatal("expected known non-member lookup to miss after delete")
	}
}
