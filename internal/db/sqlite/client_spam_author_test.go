package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

const testSpamCaseVoteResolution = "vote"

func TestConfirmedSpamClaimResetsTrustAtomically(t *testing.T) {
	t.Parallel()

	for _, knownSpam := range []bool{false, true} {
		t.Run(map[bool]string{false: testSpamCaseVoteResolution, true: "known_spam"}[knownSpam], func(t *testing.T) {
			client := newAuthorTrustClient(t)
			author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 200}
			now := time.Now().UTC()
			if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true); err != nil {
				t.Fatal(err)
			}
			spamCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: 200, MessageID: 40, CreatedAt: now, Status: db.SpamCaseStatusPending})
			if err != nil {
				t.Fatal(err)
			}
			if !knownSpam {
				if _, _, _, err := client.AddVoteIfPending(t.Context(), &db.SpamVote{CaseID: spamCase.ID, VoterID: 300, VotedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			var claimed *db.SpamCase
			var changed bool
			if knownSpam {
				claimed, changed, err = client.ClaimKnownSpamCase(t.Context(), spamCase.ID, now)
			} else {
				claimed, changed, err = client.ClaimSpamCaseResolution(t.Context(), spamCase.ID, 1, false, now)
			}
			if err != nil || !changed || claimed == nil || claimed.Status != db.SpamCaseStatusResolvingSpam {
				t.Fatalf("confirmation failed: case=%+v changed=%t err=%v", claimed, changed, err)
			}
			trust, err := client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || !trust.Suspended || trust.SafeMessages != 0 || trust.TrustedUntil.Valid {
				t.Fatalf("confirmed spam retained trust: trust=%+v err=%v", trust, err)
			}
			checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1)
			if err != nil || !checked {
				t.Fatalf("confirmation dropped edit protection: checked=%t err=%v", checked, err)
			}
		})
	}
}

func TestSpamCaseAuthorQueriesPreserveLegacyAndRecoverChannels(t *testing.T) {
	t.Parallel()

	client := newAuthorTrustClient(t)
	now := time.Now()
	userCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: 200, MessageID: 40, CreatedAt: now, Status: db.SpamCaseStatusPending})
	if err != nil {
		t.Fatal(err)
	}
	author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
	deadline := now.Add(time.Minute)
	channelCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 41, CreatedAt: now, ResolveAt: &deadline, Status: db.SpamCaseStatusPending})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.db.ExecContext(t.Context(), `INSERT INTO spam_cases (chat_id, user_id, author_kind, message_id, message_text, created_at, status) VALUES (-100, 200, 'sender_chat', 40, '', ?, 'pending')`, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, byMessage := range []bool{false, true} {
		var legacy, typed *db.SpamCase
		if byMessage {
			legacy, err = client.GetActiveSpamCaseByMessage(t.Context(), -100, 200, 40)
		} else {
			legacy, err = client.GetActiveSpamCase(t.Context(), -100, 200)
		}
		if err != nil || legacy == nil || legacy.ID != userCase.ID || legacy.Author() != (db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 200}) {
			t.Fatalf("legacy lookup crossed author kind: case=%+v err=%v", legacy, err)
		}
		if byMessage {
			typed, err = client.GetActiveAuthorSpamCaseByMessage(t.Context(), -100, author, 41)
		} else {
			typed, err = client.GetActiveAuthorSpamCase(t.Context(), -100, author)
		}
		if err != nil || typed == nil || typed.ID != channelCase.ID || typed.Author() != author {
			t.Fatalf("typed channel lookup failed: case=%+v err=%v", typed, err)
		}
	}
	if got, err := client.GetActiveAuthorSpamCaseByMessage(t.Context(), -100, author, 40); err != nil || got != nil {
		t.Fatalf("typed lookup crossed message: case=%+v err=%v", got, err)
	}
	if got, err := client.GetActiveAuthorSpamCase(t.Context(), -101, author); err != nil || got != nil {
		t.Fatalf("typed lookup crossed chat: case=%+v err=%v", got, err)
	}
	for _, invalid := range []db.MessageAuthor{{Kind: testUnknownAuthorKind, ID: 200}, {Kind: db.MessageAuthorUser, ID: -300}, {Kind: db.MessageAuthorSenderChat, ID: 200}} {
		if _, err := client.GetActiveAuthorSpamCase(t.Context(), -100, invalid); err == nil {
			t.Fatalf("accepted invalid author lookup: %+v", invalid)
		}
		if _, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: invalid.ID, AuthorKind: invalid.Kind, Status: db.SpamCaseStatusPending}); err == nil {
			t.Fatalf("accepted invalid case author: %+v", invalid)
		}
	}
	due, err := client.GetDueSpamCases(t.Context(), deadline.Add(time.Second))
	if err != nil || len(due) != 1 || due[0].Author() != author {
		t.Fatalf("due channel case lost identity: cases=%+v err=%v", due, err)
	}
}

func TestActiveAuthorSpamCasesIncludeResolvingWhileLegacyRemainsPendingOnly(t *testing.T) {
	t.Parallel()

	for _, status := range []string{db.SpamCaseStatusPending, db.SpamCaseStatusResolvingSpam, db.SpamCaseStatusResolvingFalsePositive, db.SpamCaseStatusSpam} {
		t.Run(status, func(t *testing.T) {
			client := newAuthorTrustClient(t)
			author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 200}
			spamCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 40, CreatedAt: time.Now(), Status: status})
			if err != nil {
				t.Fatal(err)
			}
			for _, byMessage := range []bool{false, true} {
				var legacy, typed *db.SpamCase
				if byMessage {
					legacy, err = client.GetActiveSpamCaseByMessage(t.Context(), -100, author.ID, 40)
				} else {
					legacy, err = client.GetActiveSpamCase(t.Context(), -100, author.ID)
				}
				if err != nil || (legacy != nil) != (status == db.SpamCaseStatusPending) {
					t.Fatalf("legacy status behavior changed: case=%+v err=%v", legacy, err)
				}
				if byMessage {
					typed, err = client.GetActiveAuthorSpamCaseByMessage(t.Context(), -100, author, 40)
				} else {
					typed, err = client.GetActiveAuthorSpamCase(t.Context(), -100, author)
				}
				if err != nil || (typed != nil) != (status != db.SpamCaseStatusSpam) || (typed != nil && typed.ID != spamCase.ID) {
					t.Fatalf("typed query lost active resolution: case=%+v err=%v", typed, err)
				}
			}
		})
	}
}

func TestSpamCaseTrustResetRollsBackWithCaseAndStats(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{"known", testSpamCaseVoteResolution, "recovered"} {
		t.Run(stage, func(t *testing.T) {
			client := newAuthorTrustClient(t)
			author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
			now := time.Now()
			if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true); err != nil {
				t.Fatal(err)
			}
			status := db.SpamCaseStatusPending
			if stage == "recovered" {
				status = db.SpamCaseStatusResolvingSpam
			}
			spamCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 40, CreatedAt: now, Status: status})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := client.AddVoteIfPending(t.Context(), &db.SpamVote{CaseID: spamCase.ID, VoterID: 200, VotedAt: now}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.db.ExecContext(t.Context(), `CREATE TRIGGER reject_trust_reset BEFORE UPDATE ON chat_author_trust BEGIN SELECT RAISE(ABORT, 'reset failed'); END`); err != nil {
				t.Fatal(err)
			}
			resolve := func() (bool, error) {
				switch stage {
				case "known":
					_, changed, err := client.ClaimKnownSpamCase(t.Context(), spamCase.ID, now)
					return changed, err
				case testSpamCaseVoteResolution:
					_, changed, err := client.ClaimSpamCaseResolution(t.Context(), spamCase.ID, 1, false, now)
					return changed, err
				default:
					return client.FinalizeSpamCaseResolution(t.Context(), spamCase.ID, status, db.SpamCaseStatusSpam, "test_confirmed", now)
				}
			}
			if changed, err := resolve(); changed || err == nil {
				t.Fatalf("reset failure committed case: changed=%t err=%v", changed, err)
			}
			stored, err := client.GetSpamCase(t.Context(), spamCase.ID)
			if err != nil || stored.Status != status || stored.ResolvedAt != nil {
				t.Fatalf("case transition escaped rollback: case=%+v err=%v", stored, err)
			}
			trust, err := client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || trust.SafeMessages != 1 || !trust.TrustedUntil.Valid || !trust.Suspended {
				t.Fatalf("trust escaped rollback: trust=%+v err=%v", trust, err)
			}
			if value, err := client.GetKV(t.Context(), "test_confirmed"); !errors.Is(err, sql.ErrNoRows) && (err != nil || value != "") {
				t.Fatalf("stats escaped rollback: value=%q err=%v", value, err)
			}
			if _, err := client.db.ExecContext(t.Context(), `DROP TRIGGER reject_trust_reset`); err != nil {
				t.Fatal(err)
			}
			if changed, err := resolve(); !changed || err != nil {
				t.Fatalf("failed to retry atomic confirmation: changed=%t err=%v", changed, err)
			}
			trust, err = client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || trust.SafeMessages != 0 || trust.TrustedUntil.Valid {
				t.Fatalf("retry retained trust: trust=%+v err=%v", trust, err)
			}
		})
	}
}

func TestSpamCaseFalsePositivePreservesRemainingTrustAndStaleFinalizerCannotResetIt(t *testing.T) {
	t.Parallel()

	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "unexpired", true: "expired"}[expired], func(t *testing.T) {
			client := newAuthorTrustClient(t)
			author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
			now := time.Now()
			grantedAt := now
			if expired {
				grantedAt = now.Add(-2 * time.Hour)
			}
			prior, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, grantedAt, 1, time.Hour, true)
			if err != nil {
				t.Fatal(err)
			}
			spamCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 40, CreatedAt: now, Status: db.SpamCaseStatusPending})
			if err != nil {
				t.Fatal(err)
			}
			claimed, changed, err := client.ClaimSpamCaseResolution(t.Context(), spamCase.ID, 1, true, now)
			if err != nil || !changed || claimed.Status != db.SpamCaseStatusResolvingFalsePositive {
				t.Fatalf("timeout without quorum failed: case=%+v changed=%t err=%v", claimed, changed, err)
			}
			if changed, err := client.FinalizeSpamCaseResolution(t.Context(), spamCase.ID, claimed.Status, db.SpamCaseStatusFalsePositive, "", now); err != nil || !changed {
				t.Fatalf("false-positive finalization: changed=%t err=%v", changed, err)
			}
			if changed, err := client.FinalizeSpamCaseResolution(t.Context(), spamCase.ID, db.SpamCaseStatusResolvingSpam, db.SpamCaseStatusSpam, "", now); err != nil || changed {
				t.Fatalf("stale spam finalizer won: changed=%t err=%v", changed, err)
			}
			trust, err := client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || trust.Suspended || trust.SafeMessages != 1 || !trust.TrustedUntil.Time.Equal(prior.TrustedUntil.Time) || trust.Trusted(now) == expired {
				t.Fatalf("false-positive changed prior expiry: trust=%+v prior=%+v err=%v", trust, prior, err)
			}
		})
	}
}
