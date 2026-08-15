package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
		SuccessUUID: testFirstValue,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Minute),
	}
	if _, err := client.CreateChallenge(ctx, first); err != nil {
		t.Fatalf("create first challenge: %v", err)
	}
	if changed, err := client.CompleteExternalAction(ctx, first.ChallengeID, db.ChallengeStatusPending, db.ChallengeStatusRejectPending, time.Time{}); err != nil || !changed {
		t.Fatalf("queue first challenge action: changed=%t err=%v", changed, err)
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
	if _, err := client.CreateChallenge(ctx, second); !errors.Is(err, ErrChallengeActionInProgress) {
		t.Fatalf("replace in-flight challenge error = %v, want ErrChallengeActionInProgress", err)
	}

	if deleted, err := client.DeleteChallengeInstance(ctx, first.ChallengeID, db.ChallengeStatusPending); err != nil || deleted {
		t.Fatalf("stale delete affected replacement: deleted=%t err=%v", deleted, err)
	}
	if _, _, updated, err := client.RecordWrongAttempt(ctx, first.ChallengeID, 3); err != nil || updated {
		t.Fatalf("stale answer affected replacement: updated=%t err=%v", updated, err)
	}

	loaded, err := client.GetChallengeByChatUser(ctx, first.ChatID, first.UserID)
	if err != nil {
		t.Fatalf("load replacement: %v", err)
	}
	if loaded == nil || loaded.ChallengeID != first.ChallengeID || loaded.SuccessUUID != first.SuccessUUID || loaded.Status != db.ChallengeStatusRejectPending {
		t.Fatalf("in-flight challenge was replaced: %#v", loaded)
	}
}

func TestChallengePersistsUsernameForDeferredIdentityChecks(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:    101,
		UserID:        202,
		Username:      "Deferred_User",
		ChatID:        -303,
		Status:        db.ChallengeStatusBanCheckPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	loaded, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}
	if loaded == nil || loaded.Username != challenge.Username {
		t.Fatalf("persisted username = %#v, want %q", loaded, challenge.Username)
	}
}

func TestDueChallengesLoadsBoundedPagesInStableOrder(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	for index := range challengeActionPageSize + 5 {
		challenge := &db.Challenge{CommChatID: int64(index + 1), UserID: int64(index + 1), ChatID: -int64(index + 1), Status: db.ChallengeStatusBanCheckPending, CreatedAt: now.Add(time.Duration(index) * time.Millisecond), ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
		if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
			t.Fatal(err)
		}
	}
	first, err := client.GetDueChallenges(t.Context(), now)
	if err != nil || len(first) != challengeActionPageSize {
		t.Fatalf("first due page length = %d, err=%v", len(first), err)
	}
	for _, challenge := range first {
		if deleted, err := client.DeleteChallengeInstance(t.Context(), challenge.ChallengeID, challenge.Status); err != nil || !deleted {
			t.Fatalf("delete first page challenge: deleted=%t err=%v", deleted, err)
		}
	}
	second, err := client.GetDueChallenges(t.Context(), now)
	if err != nil || len(second) != 5 {
		t.Fatalf("second due page length = %d, err=%v", len(second), err)
	}
}

func TestChallengeActionLeaseHasOneOwnerAndRecoversAfterExpiry(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dataDir := t.TempDir()
	client, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	challenge := &db.Challenge{
		CommChatID:    1,
		UserID:        2,
		ChatID:        3,
		Status:        db.ChallengeStatusRejectPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	claims := make(chan string, 2)
	for _, owner := range []string{"direct", "scheduler"} {
		wg.Go(func() {
			<-start
			_, claimed, claimErr := client.ClaimChallengeAction(ctx, challenge.ChallengeID, owner, now, now.Add(time.Minute))
			if claimErr != nil {
				claims <- "error:" + claimErr.Error()
				return
			}
			if claimed {
				claims <- owner
			}
		})
	}
	close(start)
	wg.Wait()
	close(claims)
	var winner string
	for claim := range claims {
		if strings.HasPrefix(claim, "error:") {
			t.Fatal(claim)
		}
		if winner != "" {
			t.Fatalf("multiple action owners: %q and %q", winner, claim)
		}
		winner = claim
	}
	if winner == "" {
		t.Fatal("expected one action owner")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close first client: %v", err)
	}

	reopened, err := NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, claimed, err := reopened.ClaimChallengeAction(ctx, challenge.ChallengeID, "restart", now.Add(30*time.Second), now.Add(2*time.Minute)); err != nil || claimed {
		t.Fatalf("unexpired lease was stolen: claimed=%t err=%v", claimed, err)
	}
	if leased, claimed, err := reopened.ClaimChallengeAction(ctx, challenge.ChallengeID, "restart", now.Add(2*time.Minute), now.Add(3*time.Minute)); err != nil || !claimed || leased.ActionOwner != "restart" {
		t.Fatalf("expired lease was not recovered: challenge=%#v claimed=%t err=%v", leased, claimed, err)
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

func TestChallengeRetryExhaustionMovesToReconciliationAndAllowsRejoin(t *testing.T) {
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
	leased, claimed, err := client.ClaimChallengeAction(ctx, challenge.ChallengeID, "worker", time.Now(), time.Now().Add(time.Minute))
	if err != nil || !claimed || leased == nil {
		t.Fatalf("claim exhausted action: challenge=%#v claimed=%t err=%v", leased, claimed, err)
	}
	if reconciled, err := client.ReconcileLeasedChallengeVersion(ctx, challenge.ChallengeID, "worker", leased.ActionVersion, db.ChallengeStatusRejectPending, 0, "retries exhausted", time.Now()); err != nil || !reconciled {
		t.Fatalf("persist reconciliation: reconciled=%t err=%v", reconciled, err)
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
	if retained != nil {
		t.Fatalf("reconciliation row still suppresses rejoin: %#v", retained)
	}
	reconciliations, err := client.GetChallengeReconciliations(ctx)
	if err != nil || len(reconciliations) != 1 {
		t.Fatalf("operator reconciliation record missing: records=%#v err=%v", reconciliations, err)
	}
	if reconciliations[0].ChallengeID != challenge.ChallengeID || reconciliations[0].ActionStatus != db.ChallengeStatusRejectPending || reconciliations[0].LastError != db.GatekeeperErrorRetryExhausted {
		t.Fatalf("unexpected reconciliation record: %#v", reconciliations[0])
	}
	replacement := &db.Challenge{
		CommChatID: challenge.CommChatID,
		UserID:     challenge.UserID,
		ChatID:     challenge.ChatID,
		Status:     db.ChallengeStatusPending,
		CreatedAt:  now.Add(time.Hour),
		ExpiresAt:  now.Add(2 * time.Hour),
	}
	if _, err := client.CreateChallenge(ctx, replacement); err != nil {
		t.Fatalf("create replacement after reconciliation: %v", err)
	}
}

func TestGenericChallengeDeleteCannotRemoveDurableOrLeasedAction(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:    91,
		UserID:        92,
		ChatID:        -93,
		Status:        db.ChallengeStatusApproveQueryPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create action: %v", err)
	}
	leased, claimed, err := client.ClaimChallengeAction(ctx, challenge.ChallengeID, "approval", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim action: challenge=%#v claimed=%t err=%v", leased, claimed, err)
	}
	if deleted, err := client.DeleteChallengeInstance(ctx, challenge.ChallengeID, challenge.Status); err != nil || deleted {
		t.Fatalf("generic delete removed leased action: deleted=%t err=%v", deleted, err)
	}
	stored, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil || stored == nil || stored.ActionOwner != "approval" {
		t.Fatalf("leased action audit state was lost: challenge=%#v err=%v", stored, err)
	}
}

func TestChallengeEffectFenceRejectsLateBindingAndArchivesAmbiguity(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now().UTC().Truncate(time.Millisecond)
	challenge := &db.Challenge{
		CommChatID:    101,
		UserID:        102,
		ChatID:        -103,
		Status:        db.ChallengeStatusWebAppFallbackPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create action: %v", err)
	}
	leased, claimed, err := client.ClaimChallengeAction(ctx, challenge.ChallengeID, "fallback", now, now.Add(40*time.Millisecond))
	if err != nil || !claimed {
		t.Fatalf("claim action: challenge=%#v claimed=%t err=%v", leased, claimed, err)
	}
	version, started, err := client.BeginLeasedChallengeEffect(ctx, challenge.ChallengeID, "fallback", leased.ActionVersion, challenge.Status, db.ChallengePhaseFallbackMessageStarted, now)
	if err != nil || !started {
		t.Fatalf("start effect: version=%d started=%t err=%v", version, started, err)
	}
	time.Sleep(60 * time.Millisecond)
	if bound, err := client.BindLeasedChallengeMessage(ctx, challenge.ChallengeID, "fallback", version, challenge.Status, db.ChallengePhaseFallbackMessageStarted, db.ChallengePhaseFallbackMessageDone, 777, time.Now()); err != nil || bound {
		t.Fatalf("late effect binding crossed lease fence: bound=%t err=%v", bound, err)
	}
	if reconciled, err := client.ReconcileExpiredChallengeEffect(ctx, challenge.ChallengeID, challenge.Status, db.ChallengePhaseFallbackMessageStarted, 777, "accepted send could not be bound", time.Now()); err != nil || !reconciled {
		t.Fatalf("archive ambiguous effect: reconciled=%t err=%v", reconciled, err)
	}
	records, err := client.GetChallengeReconciliations(ctx)
	if err != nil || len(records) != 1 || records[0].ActionPhase != db.ChallengePhaseFallbackMessageStarted || records[0].ArtifactMessageID != 777 {
		t.Fatalf("ambiguous effect metadata missing: records=%#v err=%v", records, err)
	}
}

func TestRejectSubeffectsAreFencedAndPersistedIndependently(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:    111,
		UserID:        112,
		ChatID:        -113,
		Status:        db.ChallengeStatusRejectPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create action: %v", err)
	}
	leased, claimed, err := client.ClaimChallengeAction(ctx, challenge.ChallengeID, "reject", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim action: challenge=%#v claimed=%t err=%v", leased, claimed, err)
	}
	version := leased.ActionVersion
	for _, phase := range []string{db.ChallengePhaseRejectProbeDone, db.ChallengePhaseRejectBanDone, db.ChallengePhaseRejectDeclineDone} {
		var changed bool
		version, changed, err = client.AdvanceLeasedChallengePhase(ctx, challenge.ChallengeID, "reject", version, challenge.Status, phase, time.Now())
		if err != nil || !changed {
			t.Fatalf("advance %s: version=%d changed=%t err=%v", phase, version, changed, err)
		}
	}
	stored, err := client.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil || stored == nil || stored.ActionPhase != db.ChallengePhaseRejectDeclineDone || stored.ActionVersion != version {
		t.Fatalf("reject progress was not durable: challenge=%#v err=%v", stored, err)
	}
}

func TestReconciliationWorkflowRedactsTokensAndUsesCAS(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:         121,
		UserID:             122,
		ChatID:             -123,
		Status:             db.ChallengeStatusApproveQueryPending,
		WebAppToken:        "secret-web-token",
		JoinRequestQueryID: "secret-query-token",
		ChallengeMessageID: 44,
		JoinMessageID:      45,
		NoticeMessageID:    46,
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Minute),
		NextAttemptAt:      sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatalf("create action: %v", err)
	}
	leased, claimed, err := client.ClaimChallengeAction(ctx, challenge.ChallengeID, "query", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim action: challenge=%#v claimed=%t err=%v", leased, claimed, err)
	}
	version, started, err := client.BeginLeasedChallengeEffect(ctx, challenge.ChallengeID, "query", leased.ActionVersion, challenge.Status, db.ChallengePhaseQueryAnswerStarted, now)
	if err != nil || !started {
		t.Fatalf("start query effect: version=%d started=%t err=%v", version, started, err)
	}
	if reconciled, err := client.ReconcileLeasedChallengeVersion(ctx, challenge.ChallengeID, "query", version, challenge.Status, 0, "ambiguous query answer", now); err != nil || !reconciled {
		t.Fatalf("reconcile action: reconciled=%t err=%v", reconciled, err)
	}
	records, err := client.GetChallengeReconciliations(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("list reconciliations: records=%#v err=%v", records, err)
	}
	record := records[0]
	if !record.JoinRequestQueryPresent || !record.WebAppTokenPresent || record.ChallengeMessageID != 44 || record.JoinMessageID != 45 || record.NoticeMessageID != 46 || record.ExpiresAt.IsZero() || record.RetentionUntil.Valid {
		t.Fatalf("incomplete reconciliation metadata: %#v", record)
	}
	if strings.Contains(fmt.Sprintf("%#v", record), "secret-query-token") || strings.Contains(fmt.Sprintf("%#v", record), "secret-web-token") {
		t.Fatalf("reconciliation exposed secret tokens: %#v", record)
	}
	if requeued, err := client.RequeueChallengeReconciliation(ctx, record.ID, record.Version, time.Now()); err == nil || requeued {
		t.Fatalf("ambiguous effect was requeued: requeued=%t err=%v", requeued, err)
	}
	if resolved, err := client.ResolveChallengeReconciliation(ctx, record.ID, record.Version, "inspected", time.Now()); err != nil || !resolved {
		t.Fatalf("resolve reconciliation: resolved=%t err=%v", resolved, err)
	}
	if resolved, err := client.ResolveChallengeReconciliation(ctx, record.ID, record.Version, "stale", time.Now()); err != nil || resolved {
		t.Fatalf("stale reconciliation CAS succeeded: resolved=%t err=%v", resolved, err)
	}
}

func TestChallengeDurableErrorsAreContentFreeAndRetentionStartsAtResolution(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now().Add(-60 * 24 * time.Hour)
	secret := "https://api.telegram.org/bot123456:SECRET/sendMessage?token=web-secret body=query-secret"
	challenge := &db.Challenge{CommChatID: 91, UserID: 92, ChatID: -93, Status: db.ChallengeStatusApproveQueryPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
	if _, err := client.CreateChallenge(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	leased, claimed, err := client.ClaimChallengeAction(ctx, challenge.ChallengeID, "owner", time.Now(), time.Now().Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim: challenge=%#v claimed=%t err=%v", leased, claimed, err)
	}
	if reconciled, err := client.ReconcileLeasedChallengeVersion(ctx, challenge.ChallengeID, "owner", leased.ActionVersion, challenge.Status, 0, secret, time.Now()); err != nil || !reconciled {
		t.Fatalf("reconcile: %t %v", reconciled, err)
	}
	records, err := client.GetChallengeReconciliations(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	if strings.Contains(fmt.Sprintf("%#v", records[0]), "SECRET") || strings.Contains(records[0].LastError, "http") {
		t.Fatalf("durable error leaked content: %#v", records[0])
	}
	if records[0].RetentionUntil.Valid {
		t.Fatalf("unresolved record aged out: %#v", records[0])
	}
	resolvedAt := time.Now()
	if resolved, err := client.ResolveChallengeReconciliation(ctx, records[0].ID, records[0].Version, "operator resolved", resolvedAt); err != nil || !resolved {
		t.Fatalf("resolve: %t %v", resolved, err)
	}
	records, err = client.GetChallengeReconciliations(ctx)
	if err != nil || !records[0].RetentionUntil.Valid || !records[0].RetentionUntil.Time.Equal(resolvedAt.Add(30*24*time.Hour)) {
		t.Fatalf("retention did not start at resolution: %#v err=%v", records, err)
	}
	if count, err := client.CleanupResolvedChallengeReconciliations(ctx, resolvedAt.Add(29*24*time.Hour)); err != nil || count != 0 {
		t.Fatalf("cleaned early: count=%d err=%v", count, err)
	}
	if count, err := client.CleanupResolvedChallengeReconciliations(ctx, resolvedAt.Add(31*24*time.Hour)); err != nil || count != 1 {
		t.Fatalf("did not clean after retention: count=%d err=%v", count, err)
	}
}

func TestBanCheckBoundaryCannotBeOverwritten(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now := time.Now()
	first := &db.Challenge{CommChatID: 11, UserID: 12, ChatID: -13, Status: db.ChallengeStatusBanCheckPending, JoinRequestQueryID: testFirstValue, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := client.CreateChallenge(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := &db.Challenge{CommChatID: 11, UserID: 12, ChatID: -13, Status: db.ChallengeStatusBanCheckPending, JoinRequestQueryID: "second", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := client.CreateChallenge(t.Context(), second); !errors.Is(err, ErrChallengeActionInProgress) {
		t.Fatalf("duplicate boundary overwrite error=%v", err)
	}
	stored, err := client.GetChallengeByChatUser(t.Context(), first.ChatID, first.UserID)
	if err != nil || stored == nil || stored.JoinRequestQueryID != testFirstValue {
		t.Fatalf("boundary overwritten: %#v err=%v", stored, err)
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
	if !loaded.ExpiresAt.Equal(expiresAt) || loaded.LastError != db.GatekeeperErrorPermission {
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

	t.Run("opened challenge remains claimable for expiry fallback but is not returned by unopened sweep", func(t *testing.T) {
		challenge := newWebAppChallenge(2002, 102, -100202, "token-opened", "query-opened")
		challenge.ExpiresAt = now.Add(-time.Second)
		if _, err := client.CreateChallenge(ctx, challenge); err != nil {
			t.Fatalf("create challenge: %v", err)
		}

		openedAt := now
		if err := client.MarkWebAppChallengeOpened(ctx, "token-opened", openedAt); err != nil {
			t.Fatalf("mark opened: %v", err)
		}

		claimed, err := client.BeginDMFallback(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("unopened claim after open: %v", err)
		}
		if claimed {
			t.Fatal("signed readiness must defeat the unopened fallback claim")
		}

		claimed, err = client.BeginExpiredWebAppFallback(ctx, challenge.ChallengeID)
		if err != nil {
			t.Fatalf("expiry fallback claim after open: %v", err)
		}
		if !claimed {
			t.Fatal("expected expiry fallback claim to remain available for an opened challenge")
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
