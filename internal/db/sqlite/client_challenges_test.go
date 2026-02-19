package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestChallengesSupportParallelJoinRequestsPerUser(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now()
	first := &db.Challenge{
		CommChatID:         1001,
		UserID:             777,
		ChatID:             -100111,
		SuccessUUID:        "uuid-first",
		ChallengeMessageID: 501,
		CreatedAt:          now,
		ExpiresAt:          now.Add(3 * time.Minute),
	}
	second := &db.Challenge{
		CommChatID:         1001,
		UserID:             777,
		ChatID:             -100222,
		SuccessUUID:        "uuid-second",
		ChallengeMessageID: 502,
		CreatedAt:          now,
		ExpiresAt:          now.Add(3 * time.Minute),
	}

	if _, err := client.CreateChallenge(ctx, first); err != nil {
		t.Fatalf("create first challenge: %v", err)
	}
	if _, err := client.CreateChallenge(ctx, second); err != nil {
		t.Fatalf("create second challenge: %v", err)
	}

	gotFirst, err := client.GetChallengeByMessage(ctx, first.CommChatID, first.UserID, first.ChallengeMessageID)
	if err != nil {
		t.Fatalf("get first challenge by message: %v", err)
	}
	if gotFirst == nil || gotFirst.ChatID != first.ChatID {
		t.Fatalf("unexpected first challenge: %#v", gotFirst)
	}

	gotSecond, err := client.GetChallengeByMessage(ctx, second.CommChatID, second.UserID, second.ChallengeMessageID)
	if err != nil {
		t.Fatalf("get second challenge by message: %v", err)
	}
	if gotSecond == nil || gotSecond.ChatID != second.ChatID {
		t.Fatalf("unexpected second challenge: %#v", gotSecond)
	}
}
