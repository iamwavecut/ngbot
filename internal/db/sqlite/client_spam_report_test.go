package sqlite

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestSetSpamCasePreVoteRestrictedOnlyUpdatesPendingCase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	resolveAt := now.Add(time.Minute)
	spamCase, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:            -100,
		UserID:            200,
		MessageID:         40,
		MessageText:       "candidate",
		CreatedAt:         now,
		ResolveAt:         &resolveAt,
		Status:            db.SpamCaseStatusPending,
		PreVoteRestricted: true,
	})
	if err != nil {
		t.Fatalf("create spam case: %v", err)
	}
	if err := client.SetSpamCasePreVoteRestricted(ctx, spamCase.ID, false); err != nil {
		t.Fatalf("clear pre-vote restriction: %v", err)
	}
	persisted, err := client.GetSpamCase(ctx, spamCase.ID)
	if err != nil {
		t.Fatalf("get spam case: %v", err)
	}
	if persisted.PreVoteRestricted {
		t.Fatal("failed restriction remained marked as applied")
	}

	claimed, ok, err := client.ClaimSpamCaseResolution(ctx, spamCase.ID, 1, true, now)
	if err != nil {
		t.Fatalf("claim spam case: %v", err)
	}
	if !ok || claimed == nil {
		t.Fatal("spam case was not claimed")
	}
	if err := client.SetSpamCasePreVoteRestricted(ctx, spamCase.ID, true); err == nil {
		t.Fatal("expected terminal-owner claim to reject stale restriction update")
	}
}

func TestClaimKnownSpamCaseIsSingleOwnerAndSurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	spamCase, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:      -100,
		UserID:      200,
		MessageID:   40,
		MessageText: "known spammer",
		CreatedAt:   now,
		Status:      db.SpamCaseStatusPending,
	})
	if err != nil {
		t.Fatalf("create known spam case: %v", err)
	}

	claimed, changed, err := client.ClaimKnownSpamCase(ctx, spamCase.ID, now)
	if err != nil || !changed {
		t.Fatalf("claim known spam case: changed=%t case=%#v err=%v", changed, claimed, err)
	}
	if claimed.Status != db.SpamCaseStatusResolvingSpam || !claimed.NextAttemptAt.Valid {
		t.Fatalf("unexpected claimed state: %#v", claimed)
	}
	if second, changed, err := client.ClaimKnownSpamCase(ctx, spamCase.ID, now.Add(time.Second)); err != nil || changed || second != nil {
		t.Fatalf("second owner claimed known spam case: changed=%t case=%#v err=%v", changed, second, err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	due, err := reopened.GetDueSpamCases(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("get due known spam cases: %v", err)
	}
	if len(due) != 1 || due[0].ID != spamCase.ID || due[0].Status != db.SpamCaseStatusResolvingSpam {
		t.Fatalf("claimed known spam case was not recoverable: %#v", due)
	}
}

func TestRemoveExpiredRestrictionsPreservesActiveRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC()
	for _, restriction := range []*db.UserRestriction{
		{ChatID: -100, UserID: 1, RestrictedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
		{ChatID: -100, UserID: 2, RestrictedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := client.AddRestriction(ctx, restriction); err != nil {
			t.Fatalf("add restriction for user %d: %v", restriction.UserID, err)
		}
	}
	if err := client.RemoveExpiredRestrictions(ctx); err != nil {
		t.Fatalf("remove expired restrictions: %v", err)
	}
	expired, err := client.GetActiveRestriction(ctx, -100, 1)
	if err != nil {
		t.Fatalf("get expired restriction: %v", err)
	}
	if expired != nil {
		t.Fatalf("expired restriction was retained: %#v", expired)
	}
	active, err := client.GetActiveRestriction(ctx, -100, 2)
	if err != nil {
		t.Fatalf("get active restriction: %v", err)
	}
	if active == nil {
		t.Fatal("active restriction was deleted")
	}
}

func TestSpamVoteAndTimeoutRaceHasOneResolutionAndOneStat(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	resolveAt := now.Add(time.Minute)
	spamCase, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:      -100,
		UserID:      200,
		MessageID:   40,
		MessageText: "candidate",
		CreatedAt:   now,
		ResolveAt:   &resolveAt,
		Status:      db.SpamCaseStatusPending,
	})
	if err != nil {
		t.Fatalf("create spam case: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var voteClaimed, timeoutClaimed bool
	var voteErr, timeoutErr error
	wg.Go(func() {
		<-start
		_, _, accepted, err := client.AddVoteIfPending(ctx, &db.SpamVote{
			CaseID:  spamCase.ID,
			VoterID: 300,
			Vote:    false,
			VotedAt: now,
		})
		if err != nil {
			voteErr = err
			return
		}
		if !accepted {
			return
		}
		_, voteClaimed, voteErr = client.ClaimSpamCaseResolution(ctx, spamCase.ID, 1, false, now)
	})
	wg.Go(func() {
		<-start
		_, timeoutClaimed, timeoutErr = client.ClaimSpamCaseResolution(ctx, spamCase.ID, 1, true, now)
	})
	close(start)
	wg.Wait()
	if voteErr != nil || timeoutErr != nil {
		t.Fatalf("resolution race errors: vote=%v timeout=%v", voteErr, timeoutErr)
	}
	if voteClaimed == timeoutClaimed {
		t.Fatalf("expected exactly one resolution claim: vote=%t timeout=%t", voteClaimed, timeoutClaimed)
	}

	claimed, err := client.GetSpamCase(ctx, spamCase.ID)
	if err != nil {
		t.Fatalf("get claimed case: %v", err)
	}
	terminal := db.SpamCaseStatusFalsePositive
	if claimed.Status == db.SpamCaseStatusResolvingSpam {
		terminal = db.SpamCaseStatusSpam
	} else if claimed.Status != db.SpamCaseStatusResolvingFalsePositive {
		t.Fatalf("unexpected claimed status: %q", claimed.Status)
	}

	const statsKey = "stats:test:resolution"
	finalized := make([]bool, 2)
	errs := make([]error, 2)
	start = make(chan struct{})
	wg = sync.WaitGroup{}
	for i := range 2 {
		index := i
		wg.Go(func() {
			<-start
			finalized[index], errs[index] = client.FinalizeSpamCaseResolution(
				ctx,
				spamCase.ID,
				claimed.Status,
				terminal,
				statsKey,
				now,
			)
		})
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("finalization errors: %#v", errs)
	}
	if finalized[0] == finalized[1] {
		t.Fatalf("expected exactly one finalizer: %#v", finalized)
	}
	value, err := client.GetKV(ctx, statsKey)
	if err != nil {
		t.Fatalf("get resolution stat: %v", err)
	}
	if value != "1" {
		t.Fatalf("expected exactly one stat increment, got %q", value)
	}
}

func TestSpamResolutionAndReportQueueSurviveReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	resolveAt := now.Add(time.Minute)
	spamCase, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:      -100,
		UserID:      200,
		MessageText: "candidate",
		CreatedAt:   now,
		ResolveAt:   &resolveAt,
		Status:      db.SpamCaseStatusPending,
	})
	if err != nil {
		t.Fatalf("create spam case: %v", err)
	}
	if err := client.AddSpamCaseReportMessage(ctx, &db.SpamCaseReportMessage{
		CaseID:    spamCase.ID,
		ChatID:    spamCase.ChatID,
		MessageID: 99,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("add report artifact: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	due, err := reopened.GetDueSpamCases(ctx, resolveAt.Add(time.Second))
	if err != nil {
		t.Fatalf("get due spam cases: %v", err)
	}
	if len(due) != 1 || due[0].ID != spamCase.ID {
		t.Fatalf("pending deadline was not recovered: %#v", due)
	}

	beforeRetention, err := reopened.GetDueSpamCaseReportMessages(ctx, now.Add(-time.Second))
	if err != nil {
		t.Fatalf("get report queue before retention: %v", err)
	}
	if len(beforeRetention) != 0 {
		t.Fatalf("report artifact became due too early: %#v", beforeRetention)
	}
	afterRetention, err := reopened.GetDueSpamCaseReportMessages(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("get report queue after retention: %v", err)
	}
	if len(afterRetention) != 1 || afterRetention[0].MessageID != 99 {
		t.Fatalf("report artifact was not recovered: %#v", afterRetention)
	}
}

func TestSpamResolutionRetryExhaustionRetainsCaseAndStopsPolling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	resolveAt := now.Add(-time.Minute)
	spamCase, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:    -100,
		UserID:    200,
		CreatedAt: now,
		ResolveAt: &resolveAt,
		Status:    db.SpamCaseStatusPending,
	})
	if err != nil {
		t.Fatalf("create spam case: %v", err)
	}
	claimed, changed, err := client.ClaimSpamCaseResolution(ctx, spamCase.ID, 1, true, now)
	if err != nil || !changed {
		t.Fatalf("claim resolution: changed=%t case=%#v err=%v", changed, claimed, err)
	}
	due, err := client.GetDueSpamCases(ctx, now.Add(time.Second))
	if err != nil || len(due) != 1 {
		t.Fatalf("claimed resolution was not recoverable: due=%#v err=%v", due, err)
	}
	if scheduled, err := client.ScheduleSpamCaseRetry(ctx, spamCase.ID, claimed.Status, time.Time{}, "retries exhausted"); err != nil || !scheduled {
		t.Fatalf("persist retry exhaustion: scheduled=%t err=%v", scheduled, err)
	}
	due, err = client.GetDueSpamCases(ctx, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("get due cases after exhaustion: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("exhausted case remained in polling queue: %#v", due)
	}
	retained, err := client.GetSpamCase(ctx, spamCase.ID)
	if err != nil {
		t.Fatalf("get retained case: %v", err)
	}
	if retained.Status != claimed.Status || retained.AttemptCount != 1 || retained.NextAttemptAt.Valid || retained.LastError == "" {
		t.Fatalf("exhausted case metadata was not retained: %#v", retained)
	}
}

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
		Status:            db.SpamCaseStatusPending,
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
		Status:            db.SpamCaseStatusPending,
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

func TestAddRestrictionRefreshesPersistedState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const (
		chatID = int64(-100)
		userID = int64(200)
	)
	now := time.Now().UTC().Truncate(time.Second)
	first := &db.UserRestriction{
		ChatID:       chatID,
		UserID:       userID,
		RestrictedAt: now,
		ExpiresAt:    now.Add(time.Hour),
		Reason:       "first",
	}
	if err := client.AddRestriction(ctx, first); err != nil {
		t.Fatalf("add first restriction: %v", err)
	}

	second := &db.UserRestriction{
		ChatID:       chatID,
		UserID:       userID,
		RestrictedAt: now.Add(time.Minute),
		ExpiresAt:    now.Add(2 * time.Hour),
		Reason:       "renewed",
	}
	if err := client.AddRestriction(ctx, second); err != nil {
		t.Fatalf("refresh restriction: %v", err)
	}

	got, err := client.GetActiveRestriction(ctx, chatID, userID)
	if err != nil {
		t.Fatalf("get refreshed restriction: %v", err)
	}
	if got == nil {
		t.Fatal("expected active restriction")
	}
	if got.Reason != second.Reason || !got.ExpiresAt.Equal(second.ExpiresAt) || !got.RestrictedAt.Equal(second.RestrictedAt) {
		t.Fatalf("restriction was not refreshed: got %#v want %#v", got, second)
	}

	missing, err := client.GetActiveRestriction(ctx, chatID, userID+1)
	if err != nil {
		t.Fatalf("get missing restriction: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected no missing restriction, got %#v", missing)
	}
}
