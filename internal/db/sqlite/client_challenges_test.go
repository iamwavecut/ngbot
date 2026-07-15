package sqlite

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestChallengeGenerationRejectsStaleOperations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now()
	first := &db.Challenge{
		CommChatID:  1,
		UserID:      2,
		ChatID:      3,
		Status:      db.ChallengeStatusPending,
		SuccessUUID: "first",
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Minute),
	}
	if _, err := client.CreateChallenge(ctx, first); err != nil {
		t.Fatalf("create first challenge: %v", err)
	}
	second := &db.Challenge{
		CommChatID:  first.CommChatID,
		UserID:      first.UserID,
		ChatID:      first.ChatID,
		Status:      db.ChallengeStatusPending,
		SuccessUUID: "second",
		CreatedAt:   now.Add(time.Second),
		ExpiresAt:   now.Add(2 * time.Minute),
	}
	if _, err := client.CreateChallenge(ctx, second); err != nil {
		t.Fatalf("create replacement challenge: %v", err)
	}
	if first.ChallengeID == second.ChallengeID {
		t.Fatalf("expected a new generation token, got %q", first.ChallengeID)
	}

	if deleted, err := client.DeleteChallengeInstance(ctx, first.ChallengeID, db.ChallengeStatusPending); err != nil || deleted {
		t.Fatalf("stale delete affected replacement: deleted=%t err=%v", deleted, err)
	}
	if _, _, updated, err := client.RecordWrongAttempt(ctx, first.ChallengeID, 3); err != nil || updated {
		t.Fatalf("stale answer affected replacement: updated=%t err=%v", updated, err)
	}

	loaded, err := client.GetChallengeByChatUser(ctx, second.ChatID, second.UserID)
	if err != nil {
		t.Fatalf("load replacement: %v", err)
	}
	if loaded == nil || loaded.ChallengeID != second.ChallengeID || loaded.SuccessUUID != second.SuccessUUID || loaded.Attempts != 0 {
		t.Fatalf("replacement challenge was changed: %#v", loaded)
	}
}

func TestChallengeCorrectWrongRaceHasSingleTerminalOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	for iteration := range 32 {
		now := time.Now()
		challenge := &db.Challenge{
			CommChatID:  int64(iteration + 1),
			UserID:      42,
			ChatID:      -100 - int64(iteration),
			Status:      db.ChallengeStatusPending,
			SuccessUUID: "correct",
			Attempts:    2,
			CreatedAt:   now,
			ExpiresAt:   now.Add(time.Minute),
		}
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("iteration %d create challenge: %v", iteration, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var claimed, wrongUpdated bool
		var claimErr, wrongErr error
		wg.Go(func() {
			<-start
			claimed, claimErr = client.ClaimForApproval(ctx, challenge.ChallengeID)
		})
		wg.Go(func() {
			<-start
			_, _, wrongUpdated, wrongErr = client.RecordWrongAttempt(ctx, challenge.ChallengeID, 3)
		})
		close(start)
		wg.Wait()
		if claimErr != nil || wrongErr != nil {
			t.Fatalf("iteration %d race errors: claim=%v wrong=%v", iteration, claimErr, wrongErr)
		}
		if claimed == wrongUpdated {
			t.Fatalf("iteration %d expected exactly one terminal owner: claimed=%t wrong=%t", iteration, claimed, wrongUpdated)
		}

		loaded, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
		if err != nil {
			t.Fatalf("iteration %d load challenge: %v", iteration, err)
		}
		if claimed && (loaded.Status != db.ChallengeStatusApproveQueryPending || loaded.Attempts != 2) {
			t.Fatalf("iteration %d approved challenge was resurrected or mutated: %#v", iteration, loaded)
		}
		if wrongUpdated && (loaded.Status != db.ChallengeStatusRejectPending || loaded.Attempts != 3) {
			t.Fatalf("iteration %d wrong attempt was lost: %#v", iteration, loaded)
		}
	}
}

func TestDueChallengeRetrySurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	challenge := &db.Challenge{
		CommChatID:         1,
		UserID:             2,
		ChatID:             3,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        "correct",
		JoinRequestQueryID: "query",
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Minute),
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if claimed, err := client.ClaimForApproval(ctx, challenge.ChallengeID); err != nil || !claimed {
		t.Fatalf("claim challenge: claimed=%t err=%v", claimed, err)
	}
	retryAt := now.Add(30 * time.Second)
	if scheduled, err := client.ScheduleChallengeRetry(ctx, challenge.ChallengeID, db.ChallengeStatusApproveQueryPending, retryAt, "temporary failure"); err != nil || !scheduled {
		t.Fatalf("schedule retry: scheduled=%t err=%v", scheduled, err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close first client: %v", err)
	}

	reopened, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	before, err := reopened.GetDueChallenges(ctx, retryAt.Add(-time.Second))
	if err != nil {
		t.Fatalf("get retries before due: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("retry became due too early: %#v", before)
	}
	after, err := reopened.GetDueChallenges(ctx, retryAt.Add(time.Second))
	if err != nil {
		t.Fatalf("get due retries after reopen: %v", err)
	}
	if len(after) != 1 || after[0].ChallengeID != challenge.ChallengeID || after[0].AttemptCount != 1 || after[0].LastError == "" {
		t.Fatalf("durable retry metadata was not recovered: %#v", after)
	}
}

func TestChallengeRetryExhaustionRetainsRowAndStopsPolling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	challenge := &db.Challenge{
		CommChatID: 1,
		UserID:     2,
		ChatID:     3,
		Status:     db.ChallengeStatusPending,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Minute),
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	changed, err := client.CompleteExternalAction(ctx, challenge.ChallengeID, db.ChallengeStatusPending, db.ChallengeStatusRejectPending, time.Time{})
	if err != nil || !changed {
		t.Fatalf("claim durable action: changed=%t err=%v", changed, err)
	}
	due, err := client.GetDueChallenges(ctx, time.Now().Add(time.Second))
	if err != nil || len(due) != 1 {
		t.Fatalf("durable action was not recoverable after claim: due=%#v err=%v", due, err)
	}
	if scheduled, err := client.ScheduleChallengeRetry(ctx, challenge.ChallengeID, db.ChallengeStatusRejectPending, time.Time{}, "retries exhausted"); err != nil || !scheduled {
		t.Fatalf("persist retry exhaustion: scheduled=%t err=%v", scheduled, err)
	}
	due, err = client.GetDueChallenges(ctx, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("get due challenges after exhaustion: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("exhausted challenge remained in polling queue: %#v", due)
	}
	retained, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("get retained challenge: %v", err)
	}
	if retained == nil || retained.Status != db.ChallengeStatusRejectPending || retained.AttemptCount != 1 || retained.NextAttemptAt.Valid || retained.LastError == "" {
		t.Fatalf("exhausted challenge metadata was not retained: %#v", retained)
	}
}

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

	changed, err := client.CompleteExternalAction(ctx, challenge.ChallengeID, db.ChallengeStatusPending, db.ChallengeStatusPassedWaitingMemberJoin, now.Add(5*time.Minute))
	if err != nil || !changed {
		t.Fatalf("transition challenge: changed=%t err=%v", changed, err)
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

func TestChallengeNoPrivilegesNoticeLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	challenge := &db.Challenge{
		CommChatID:         -100,
		UserID:             200,
		ChatID:             -100,
		Status:             db.ChallengeStatusRejectPending,
		SuccessUUID:        "no-rights",
		ChallengeMessageID: 40,
		UserRestricted:     false,
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Minute),
		NextAttemptAt:      sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	expiresAt := now.Add(30 * time.Minute)
	changed, err := client.CompleteChallengeWithoutPrivileges(
		ctx,
		challenge.ChallengeID,
		db.ChallengeStatusRejectPending,
		77,
		expiresAt,
		"CHAT_ADMIN_REQUIRED",
	)
	if err != nil || !changed {
		t.Fatalf("complete no-rights challenge: changed=%t err=%v", changed, err)
	}
	loaded, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("load no-rights challenge: %v", err)
	}
	if loaded == nil || loaded.Status != db.ChallengeStatusNoPrivilegesNotice || loaded.NoticeMessageID != 77 || loaded.UserRestricted || loaded.NextAttemptAt.Valid {
		t.Fatalf("unexpected no-rights challenge state: %#v", loaded)
	}
	if !loaded.ExpiresAt.Equal(expiresAt) || loaded.LastError != "CHAT_ADMIN_REQUIRED" {
		t.Fatalf("unexpected no-rights challenge metadata: %#v", loaded)
	}

	expired, err := client.GetExpiredChallenges(ctx, expiresAt.Add(-time.Second))
	if err != nil || len(expired) != 0 {
		t.Fatalf("notice expired too early: expired=%#v err=%v", expired, err)
	}
	expired, err = client.GetExpiredChallenges(ctx, expiresAt.Add(time.Second))
	if err != nil || len(expired) != 1 || expired[0].ChallengeID != challenge.ChallengeID {
		t.Fatalf("notice was not exposed after retention: expired=%#v err=%v", expired, err)
	}
}

func TestExpiredChallengeActionRemainsInRetryQueueOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	challenge := &db.Challenge{
		CommChatID:    -100,
		UserID:        200,
		ChatID:        -100,
		Status:        db.ChallengeStatusRejectPending,
		SuccessUUID:   "due-only",
		CreatedAt:     now.Add(-time.Hour),
		ExpiresAt:     now.Add(-time.Minute),
		NextAttemptAt: sql.NullTime{Time: now.Add(time.Minute), Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	expired, err := client.GetExpiredChallenges(ctx, now)
	if err != nil || len(expired) != 0 {
		t.Fatalf("durable action leaked into expiry worker: expired=%#v err=%v", expired, err)
	}
	due, err := client.GetDueChallenges(ctx, now.Add(2*time.Minute))
	if err != nil || len(due) != 1 || due[0].ChallengeID != challenge.ChallengeID {
		t.Fatalf("durable action was not retained in retry queue: due=%#v err=%v", due, err)
	}
}

func TestChallengeWebAppTokenLookup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:         0,
		UserID:             777,
		ChatID:             -100333,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        "uuid-pending",
		WebAppToken:        "web-token",
		JoinRequestQueryID: "join-query",
		CaptchaPrompt:      "poodle",
		CaptchaOptionsJSON: `[{"id":"uuid-pending","symbol":"A"}]`,
		ChallengeMessageID: 0,
		CreatedAt:          now,
		ExpiresAt:          now.Add(5 * time.Minute),
	}

	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	got, err := client.GetChallengeByWebAppToken(ctx, "web-token")
	if err != nil {
		t.Fatalf("get challenge by web app token: %v", err)
	}
	if got == nil {
		t.Fatal("expected challenge lookup by web app token to match")
	}
	if got.JoinRequestQueryID != challenge.JoinRequestQueryID || got.CaptchaPrompt != challenge.CaptchaPrompt {
		t.Fatalf("unexpected web app challenge: %#v", got)
	}
}

func TestChallengeUserLanguageRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	challenge := &db.Challenge{
		CommChatID:   3001,
		UserID:       303,
		ChatID:       -100303,
		Status:       db.ChallengeStatusPending,
		SuccessUUID:  "uuid-lang",
		UserLanguage: "ru",
		CreatedAt:    now,
		ExpiresAt:    now.Add(3 * time.Minute),
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	loaded, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("get challenge: %v", err)
	}
	if loaded == nil || loaded.UserLanguage != "ru" {
		t.Fatalf("expected user_language ru to round-trip, got %#v", loaded)
	}
}

func TestWebAppChallengeClaimAndOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)

	newWebAppChallenge := func(commChatID, userID, chatID int64, token, queryID string) *db.Challenge {
		return &db.Challenge{
			CommChatID:         commChatID,
			UserID:             userID,
			ChatID:             chatID,
			Status:             db.ChallengeStatusPending,
			SuccessUUID:        "uuid-webapp",
			WebAppToken:        token,
			JoinRequestQueryID: queryID,
			CaptchaPrompt:      "poodle",
			CaptchaOptionsJSON: `[{"id":"uuid-webapp","symbol":"A"}]`,
			CreatedAt:          now.Add(-2 * time.Minute),
			ExpiresAt:          now.Add(5 * time.Minute),
		}
	}

	t.Run("claim returns true then false on second attempt", func(t *testing.T) {
		challenge := newWebAppChallenge(2001, 101, -100201, "token-claim", "query-claim")
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		claimed, err := client.BeginDMFallback(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if !claimed {
			t.Fatal("expected first claim to return true")
		}

		got, err := client.GetChallengeByWebAppToken(ctx, "token-claim")
		if err != nil {
			t.Fatalf("get after claim: %v", err)
		}
		if got == nil {
			t.Fatal("expected challenge to still exist")
		}
		if got.Status != db.ChallengeStatusWebAppFallbackPending {
			t.Fatalf("expected status %q after claim, got %q", db.ChallengeStatusWebAppFallbackPending, got.Status)
		}

		claimed, err = client.BeginDMFallback(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("second claim: %v", err)
		}
		if claimed {
			t.Fatal("expected second claim to return false")
		}
	})

	t.Run("approval claim returns true then false on second attempt", func(t *testing.T) {
		challenge := newWebAppChallenge(2011, 111, -100211, "token-approve", "query-approve")
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		claimed, err := client.ClaimForApproval(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("first approval claim: %v", err)
		}
		if !claimed {
			t.Fatal("expected first approval claim to return true")
		}

		got, err := client.GetChallengeByWebAppToken(ctx, "token-approve")
		if err != nil {
			t.Fatalf("get after approval claim: %v", err)
		}
		if got == nil {
			t.Fatal("expected challenge to still exist")
		}
		if got.Status != db.ChallengeStatusApproveQueryPending {
			t.Fatalf("expected status %q after approval claim, got %q", db.ChallengeStatusApproveQueryPending, got.Status)
		}

		claimed, err = client.ClaimForApproval(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("second approval claim: %v", err)
		}
		if claimed {
			t.Fatal("expected second approval claim to return false")
		}
	})

	t.Run("approval claim loses to a fallback-claimed row", func(t *testing.T) {
		challenge := newWebAppChallenge(2012, 112, -100212, "token-approve-fallback", "query-approve-fallback")
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		claimed, err := client.BeginDMFallback(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("fallback claim: %v", err)
		}
		if !claimed {
			t.Fatal("expected fallback claim to win")
		}

		claimed, err = client.ClaimForApproval(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("approval claim after fallback: %v", err)
		}
		if claimed {
			t.Fatal("expected approval claim to return false once fallback owns the row")
		}

		got, err := client.GetChallengeByWebAppToken(ctx, "token-approve-fallback")
		if err != nil {
			t.Fatalf("get after fallback claim: %v", err)
		}
		if got.Status != db.ChallengeStatusWebAppFallbackPending {
			t.Fatalf("expected status to remain %q, got %q", db.ChallengeStatusWebAppFallbackPending, got.Status)
		}
	})

	t.Run("opened challenge is not claimable and not returned by GetUnopenedWebAppChallenges", func(t *testing.T) {
		challenge := newWebAppChallenge(2002, 102, -100202, "token-opened", "query-opened")
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		openedAt := now
		if err := client.MarkWebAppChallengeOpened(ctx, "token-opened", openedAt); err != nil {
			t.Fatalf("mark opened: %v", err)
		}

		claimed, err := client.BeginDMFallback(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("claim after open: %v", err)
		}
		if claimed {
			t.Fatal("expected claim to return false for already-opened challenge")
		}

		unopened, err := client.GetUnopenedWebAppChallenges(ctx, now)
		if err != nil {
			t.Fatalf("get unopened: %v", err)
		}
		for _, ch := range unopened {
			if ch.WebAppToken == "token-opened" {
				t.Fatal("opened challenge must not appear in GetUnopenedWebAppChallenges")
			}
		}
	})

	t.Run("pending unopened challenge appears in sweep and WebAppOpenedAt round-trips", func(t *testing.T) {
		challenge := newWebAppChallenge(2003, 103, -100203, "token-sweep", "query-sweep")
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		unopened, err := client.GetUnopenedWebAppChallenges(ctx, now)
		if err != nil {
			t.Fatalf("get unopened before mark: %v", err)
		}
		found := false
		for _, ch := range unopened {
			if ch.WebAppToken == "token-sweep" {
				found = true
				if ch.WebAppOpenedAt.Valid {
					t.Fatal("expected WebAppOpenedAt to be NULL before mark")
				}
			}
		}
		if !found {
			t.Fatal("expected pending challenge to appear in GetUnopenedWebAppChallenges")
		}

		openedAt := now
		if err := client.MarkWebAppChallengeOpened(ctx, "token-sweep", openedAt); err != nil {
			t.Fatalf("mark opened: %v", err)
		}

		got, err := client.GetChallengeByWebAppToken(ctx, "token-sweep")
		if err != nil {
			t.Fatalf("get after mark: %v", err)
		}
		if got == nil {
			t.Fatal("expected challenge to exist after mark")
		}
		if !got.WebAppOpenedAt.Valid {
			t.Fatal("expected WebAppOpenedAt.Valid to be true after MarkWebAppChallengeOpened")
		}
		if !got.WebAppOpenedAt.Time.Equal(openedAt) {
			t.Fatalf("WebAppOpenedAt mismatch: got %v want %v", got.WebAppOpenedAt.Time, openedAt)
		}

		unopened, err = client.GetUnopenedWebAppChallenges(ctx, now)
		if err != nil {
			t.Fatalf("get unopened after mark: %v", err)
		}
		for _, ch := range unopened {
			if ch.WebAppToken == "token-sweep" {
				t.Fatal("marked-opened challenge must not appear in GetUnopenedWebAppChallenges")
			}
		}
	})

	t.Run("MarkWebAppChallengeOpened is idempotent on already-opened row", func(t *testing.T) {
		challenge := newWebAppChallenge(2004, 104, -100204, "token-idem", "query-idem")
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		first := now
		if err := client.MarkWebAppChallengeOpened(ctx, "token-idem", first); err != nil {
			t.Fatalf("first mark: %v", err)
		}

		second := now.Add(time.Minute)
		if err := client.MarkWebAppChallengeOpened(ctx, "token-idem", second); err != nil {
			t.Fatalf("second mark: %v", err)
		}

		got, err := client.GetChallengeByWebAppToken(ctx, "token-idem")
		if err != nil {
			t.Fatalf("get after second mark: %v", err)
		}
		if !got.WebAppOpenedAt.Valid {
			t.Fatal("expected WebAppOpenedAt to be valid")
		}
		if !got.WebAppOpenedAt.Time.Equal(first) {
			t.Fatalf("expected first open time to be preserved, got %v", got.WebAppOpenedAt.Time)
		}
	})

	t.Run("CreateChallenge round-trips WebAppOpenedAt when set", func(t *testing.T) {
		openedAt := now
		challenge := newWebAppChallenge(2005, 105, -100205, "token-rt", "query-rt")
		challenge.WebAppOpenedAt = sql.NullTime{Time: openedAt, Valid: true}
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge with opened at: %v", err)
		}

		got, err := client.GetChallengeByWebAppToken(ctx, "token-rt")
		if err != nil {
			t.Fatalf("get challenge: %v", err)
		}
		if got == nil {
			t.Fatal("expected challenge")
		}
		if !got.WebAppOpenedAt.Valid {
			t.Fatal("expected WebAppOpenedAt.Valid after round-trip")
		}
		if !got.WebAppOpenedAt.Time.Equal(openedAt) {
			t.Fatalf("WebAppOpenedAt time mismatch: got %v want %v", got.WebAppOpenedAt.Time, openedAt)
		}
	})
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
