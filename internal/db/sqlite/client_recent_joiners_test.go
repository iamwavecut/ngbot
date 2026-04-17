package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestAddChatRecentJoinerUpsertsMessageIDFromZeroToServiceMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	firstJoinAt := time.Now().Add(-time.Minute)
	secondJoinAt := time.Now()

	if _, err := client.AddChatRecentJoiner(ctx, &db.RecentJoiner{
		ChatID:        100,
		UserID:        200,
		Username:      "neo",
		JoinedAt:      firstJoinAt,
		JoinMessageID: 0,
	}); err != nil {
		t.Fatalf("add chat_member joiner: %v", err)
	}

	if _, err := client.AddChatRecentJoiner(ctx, &db.RecentJoiner{
		ChatID:        100,
		UserID:        200,
		Username:      "neo_updated",
		JoinedAt:      secondJoinAt,
		JoinMessageID: 55,
	}); err != nil {
		t.Fatalf("add new_chat_members joiner: %v", err)
	}

	joiner := loadOnlyRecentJoiner(t, ctx, client, 100)
	if joiner.JoinMessageID != 55 {
		t.Fatalf("expected join message id 55, got %d", joiner.JoinMessageID)
	}
	if joiner.Username != "neo_updated" {
		t.Fatalf("expected username to be updated, got %q", joiner.Username)
	}
	if !joiner.JoinedAt.After(firstJoinAt) {
		t.Fatalf("expected joined_at to be refreshed, got %s", joiner.JoinedAt)
	}
}

func TestAddChatRecentJoinerUpsertKeepsExistingMessageIDWhenNewValueIsZero(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.AddChatRecentJoiner(ctx, &db.RecentJoiner{
		ChatID:        100,
		UserID:        200,
		Username:      "neo",
		JoinedAt:      time.Now().Add(-time.Minute),
		JoinMessageID: 55,
	}); err != nil {
		t.Fatalf("add service-message joiner: %v", err)
	}

	if _, err := client.AddChatRecentJoiner(ctx, &db.RecentJoiner{
		ChatID:        100,
		UserID:        200,
		Username:      "neo_refreshed",
		JoinedAt:      time.Now(),
		JoinMessageID: 0,
	}); err != nil {
		t.Fatalf("add chat_member joiner: %v", err)
	}

	joiner := loadOnlyRecentJoiner(t, ctx, client, 100)
	if joiner.JoinMessageID != 55 {
		t.Fatalf("expected join message id to stay 55, got %d", joiner.JoinMessageID)
	}
	if joiner.Username != "neo_refreshed" {
		t.Fatalf("expected username to refresh, got %q", joiner.Username)
	}
}

func TestAddChatRecentJoinerRejoinResetsProcessedAndSpamFlags(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if _, err := client.AddChatRecentJoiner(ctx, &db.RecentJoiner{
		ChatID:        100,
		UserID:        200,
		Username:      "neo",
		JoinedAt:      time.Now().Add(-2 * time.Minute),
		JoinMessageID: 55,
	}); err != nil {
		t.Fatalf("add initial joiner: %v", err)
	}

	if err := client.ProcessRecentJoiner(ctx, 100, 200, true); err != nil {
		t.Fatalf("process recent joiner: %v", err)
	}

	processedJoiner := loadOnlyRecentJoiner(t, ctx, client, 100)
	if !processedJoiner.Processed || !processedJoiner.IsSpammer {
		t.Fatalf("expected processed spammer state before rejoin, got %+v", processedJoiner)
	}

	if _, err := client.AddChatRecentJoiner(ctx, &db.RecentJoiner{
		ChatID:        100,
		UserID:        200,
		Username:      "neo_returned",
		JoinedAt:      time.Now(),
		JoinMessageID: 66,
	}); err != nil {
		t.Fatalf("add rejoiner: %v", err)
	}

	joiner := loadOnlyRecentJoiner(t, ctx, client, 100)
	if joiner.Processed {
		t.Fatal("expected processed=false after rejoin")
	}
	if joiner.IsSpammer {
		t.Fatal("expected is_spammer=false after rejoin")
	}
	if joiner.JoinMessageID != 66 {
		t.Fatalf("expected join message id 66 after rejoin, got %d", joiner.JoinMessageID)
	}
}

func loadOnlyRecentJoiner(t *testing.T, ctx context.Context, client *sqliteClient, chatID int64) *db.RecentJoiner {
	t.Helper()

	joiners, err := client.GetChatRecentJoiners(ctx, chatID)
	if err != nil {
		t.Fatalf("get chat recent joiners: %v", err)
	}
	if len(joiners) != 1 {
		t.Fatalf("expected one recent joiner row, got %d", len(joiners))
	}
	return joiners[0]
}
