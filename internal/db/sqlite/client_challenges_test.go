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
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        "uuid-first",
		ChallengeMessageID: 501,
		CreatedAt:          now,
		ExpiresAt:          now.Add(3 * time.Minute),
	}
	second := &db.Challenge{
		CommChatID:         1001,
		UserID:             777,
		ChatID:             -100222,
		Status:             db.ChallengeStatusPending,
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

func TestChallengeStatusLookupAndExpiryLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:         9001,
		UserID:             777,
		ChatID:             -100333,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        "uuid-pending",
		ChallengeMessageID: 503,
		CreatedAt:          now,
		ExpiresAt:          now.Add(5 * time.Minute),
	}

	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	gotByChatUser, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("get challenge by chat user: %v", err)
	}
	if gotByChatUser == nil {
		t.Fatal("expected challenge lookup by chat user to match")
	}
	if gotByChatUser.Status != db.ChallengeStatusPending {
		t.Fatalf("unexpected challenge status: got %q want %q", gotByChatUser.Status, db.ChallengeStatusPending)
	}

	challenge.Status = db.ChallengeStatusPassedWaitingMemberJoin
	challenge.ChallengeMessageID = 0
	challenge.ExpiresAt = now.Add(5 * time.Minute)
	if err := client.UpdateChallenge(ctx, challenge); err != nil {
		t.Fatalf("update challenge: %v", err)
	}

	gotByChatUser, err = client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("get updated challenge by chat user: %v", err)
	}
	if gotByChatUser == nil {
		t.Fatal("expected updated challenge lookup by chat user to match")
	}
	if gotByChatUser.Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("unexpected updated challenge status: got %q want %q", gotByChatUser.Status, db.ChallengeStatusPassedWaitingMemberJoin)
	}

	gotByMessage, err := client.GetChallengeByMessage(ctx, challenge.CommChatID, challenge.UserID, 503)
	if err != nil {
		t.Fatalf("get challenge by message after handoff: %v", err)
	}
	if gotByMessage != nil {
		t.Fatalf("expected passed handoff challenge to be hidden from message lookup, got %#v", gotByMessage)
	}

	expired, err := client.GetExpiredChallenges(ctx, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("get expired challenges before ttl: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("expected no expired challenges before ttl, got %d", len(expired))
	}

	expired, err = client.GetExpiredChallenges(ctx, now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("get expired challenges after ttl: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected one expired challenge after ttl, got %d", len(expired))
	}
	if expired[0].Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("unexpected expired challenge status: got %q want %q", expired[0].Status, db.ChallengeStatusPassedWaitingMemberJoin)
	}
}

func TestGetPassedJoinRequestChallengeByChatUserIgnoresNewerPublicChallenge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now()
	handoff := &db.Challenge{
		CommChatID:         9001,
		UserID:             777,
		ChatID:             -100333,
		Status:             db.ChallengeStatusPassedWaitingMemberJoin,
		SuccessUUID:        "uuid-handoff",
		ChallengeMessageID: 0,
		CreatedAt:          now,
		ExpiresAt:          now.Add(5 * time.Minute),
	}
	publicChallenge := &db.Challenge{
		CommChatID:         -100333,
		UserID:             777,
		ChatID:             -100333,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        "uuid-public",
		ChallengeMessageID: 504,
		CreatedAt:          now.Add(time.Minute),
		ExpiresAt:          now.Add(5 * time.Minute),
	}

	if _, err := client.CreateChallenge(ctx, handoff); err != nil {
		t.Fatalf("create handoff challenge: %v", err)
	}
	if _, err := client.CreateChallenge(ctx, publicChallenge); err != nil {
		t.Fatalf("create public challenge: %v", err)
	}

	latest, err := client.GetChallengeByChatUser(ctx, handoff.ChatID, handoff.UserID)
	if err != nil {
		t.Fatalf("get latest challenge by chat user: %v", err)
	}
	if latest == nil || latest.CommChatID != publicChallenge.CommChatID {
		t.Fatalf("expected latest generic lookup to return public challenge, got %#v", latest)
	}

	got, err := client.GetPassedJoinRequestChallengeByChatUser(ctx, handoff.ChatID, handoff.UserID)
	if err != nil {
		t.Fatalf("get passed join request challenge by chat user: %v", err)
	}
	if got == nil {
		t.Fatal("expected handoff challenge lookup to match")
	}
	if got.CommChatID != handoff.CommChatID || got.Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("unexpected handoff challenge: %#v", got)
	}
}
