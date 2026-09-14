package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

const testTelegramErrorBadGateway = "Bad Gateway"

type retryAuthorContextStore struct {
	gatekeeperStore
	fail bool
}

func (s *retryAuthorContextStore) DeleteAuthorMessageContext(ctx context.Context, chatID int64, author db.MessageAuthor) error {
	if s.fail {
		return errors.New("forced context persistence failure")
	}
	return s.gatekeeperStore.DeleteAuthorMessageContext(ctx, chatID, author)
}

func (s *retryAuthorContextStore) DeleteMessageContext(ctx context.Context, chatID int64, messageID int) error {
	if s.fail {
		return errors.New("forced context persistence failure")
	}
	return s.gatekeeperStore.DeleteMessageContext(ctx, chatID, messageID)
}

func TestExplicitDeletionRetriesContextAfterTelegramAlreadyDeleted(t *testing.T) {
	t.Parallel()
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "saved context", SentAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodDeleteMessage {
			t.Fatalf("unexpected method: %s", method)
		}
		calls++
		if calls > 1 {
			return &testBotAPIError{code: 400, description: "message to delete not found"}
		}
		return true
	})
	store := &retryAuthorContextStore{gatekeeperStore: client, fail: true}
	if err := bot.DeleteChatMessageAndContext(t.Context(), botAPI, store, -100, 10); err == nil {
		t.Fatal("expected persistence failure")
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved == nil {
		t.Fatalf("failed persistence changed context: saved=%+v err=%v", saved, err)
	}
	store.fail = false
	if err := bot.DeleteChatMessageAndContext(t.Context(), botAPI, store, -100, 10); err != nil {
		t.Fatal(err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved != nil {
		t.Fatalf("retry retained deleted context: saved=%+v err=%v", saved, err)
	}
}

func TestReactionUserRevokeClearsHistoryOnlyAfterSuccessfulBan(t *testing.T) {
	t.Parallel()
	for _, succeeds := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "succeeded"}[succeeds], func(t *testing.T) {
			f := newTrustFixture(t)
			record := &db.MessageContext{ChatID: f.chat.ID, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: f.user.ID, Text: "earlier contribution", SentAt: f.now, UpdatedAt: f.now}
			if err := f.store.UpsertMessageContext(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			f.reactor.bot = newTestBotAPI(t, func(method string, _ *http.Request) any {
				switch method {
				case "deleteAllMessageReactions":
					return true
				case testTelegramMethodBanChatMember:
					if !succeeds {
						return &testBotAPIError{code: 500, description: testTelegramErrorBadGateway}
					}
					return true
				default:
					t.Fatalf("unexpected method: %s", method)
					return nil
				}
			})
			err := f.reactor.punishReactionUser(t.Context(), f.chat.ID, 40, f.user.ID, f.reactor.getLogEntry())
			if (err == nil) != succeeds {
				t.Fatalf("unexpected ban result: %v", err)
			}
			if saved, err := f.store.MessageContext(t.Context(), f.chat.ID, 10); err != nil || (saved == nil) != succeeds {
				t.Fatalf("history deletion does not match revoke: saved=%+v err=%v", saved, err)
			}
		})
	}
}

func TestCAPTCHARevokeContextRetryDoesNotRepeatBan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client, err := sqlite.NewSQLiteClient(t.Context(), dir, "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	settings := db.DefaultSettings(-100)
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	message := &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "earlier contribution", SentAt: now, UpdatedAt: now}
	if err := client.UpsertMessageContext(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	challenge, err := client.CreateChallenge(t.Context(), &db.Challenge{ChatID: -100, CommChatID: -100, UserID: 200, UserRestricted: true, Status: db.ChallengeStatusRejectPending, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	bans := 0
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodBanChatMember {
			t.Fatalf("unexpected method: %s", method)
		}
		bans++
		return true
	})
	store := &retryAuthorContextStore{gatekeeperStore: client, fail: true}
	gatekeeper := &Gatekeeper{bot: botAPI, s: &testBotService{settings: settings}, store: store, banChecker: &testGatekeeperBanChecker{}}
	if err := gatekeeper.processChallengeActionWithoutStats(t.Context(), challenge); err == nil {
		t.Fatal("expected context persistence failure")
	}
	stored, err := client.GetChallengeByChatUser(t.Context(), -100, 200)
	if err != nil || stored == nil || stored.ActionPhase != "reject_context_pending" || stored.AttemptCount != 1 || bans != 1 {
		t.Fatalf("completed revoke lost cleanup retry boundary: challenge=%+v bans=%d err=%v", stored, bans, err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved == nil {
		t.Fatalf("failed cleanup mutated context: saved=%+v err=%v", saved, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = sqlite.NewSQLiteClient(t.Context(), dir, "context.db")
	if err != nil {
		t.Fatal(err)
	}
	gatekeeper.store = client
	gatekeeper.now = func() time.Time { return stored.NextAttemptAt.Time.Add(time.Second) }
	checker := gatekeeper.banChecker.(*testGatekeeperBanChecker)
	checker.moderationErr = errors.New("transient capability failure after context cleanup")
	if err := gatekeeper.processChallengeActionWithoutStats(t.Context(), stored); err == nil {
		t.Fatal("expected capability failure after successful cleanup")
	}
	stored, err = client.GetChallengeByChatUser(t.Context(), -100, 200)
	if err != nil || stored == nil || stored.ActionPhase != db.ChallengePhaseRejectBanDone || stored.AttemptCount != 2 || bans != 1 {
		t.Fatalf("completed revoke lost boundary after cleanup: challenge=%+v bans=%d err=%v", stored, bans, err)
	}
	checker.moderationErr = nil
	if err := gatekeeper.processChallengeActionWithoutStats(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	if bans != 1 {
		t.Fatalf("context retry repeated Telegram ban: %d", bans)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved != nil {
		t.Fatalf("retry retained revoked history: saved=%+v err=%v", saved, err)
	}
	if saved, err := client.GetChallengeByChatUser(t.Context(), -100, 200); err != nil || saved != nil {
		t.Fatalf("cleanup retry did not finish challenge: saved=%+v err=%v", saved, err)
	}
}

func TestCAPTCHADeactivatedUserDoesNotImplyRevokedHistory(t *testing.T) {
	t.Parallel()
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	settings := db.DefaultSettings(-100)
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "undeleted history", SentAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	challenge, err := client.CreateChallenge(t.Context(), &db.Challenge{ChatID: -100, CommChatID: -100, UserID: 200, UserRestricted: true, Status: db.ChallengeStatusRejectPending, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodBanChatMember {
			t.Fatalf("unexpected method: %s", method)
		}
		return &testBotAPIError{code: 400, description: "USER IS DEACTIVATED"}
	})
	gatekeeper := &Gatekeeper{bot: botAPI, s: &testBotService{settings: settings}, store: client, banChecker: &testGatekeeperBanChecker{}}
	if err := gatekeeper.processChallengeActionWithoutStats(t.Context(), challenge); err != nil {
		t.Fatal(err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved == nil {
		t.Fatalf("deactivated response purged history: saved=%+v err=%v", saved, err)
	}
	if saved, err := client.GetChallengeByChatUser(t.Context(), -100, 200); err != nil || saved != nil {
		t.Fatalf("deactivated challenge did not finish: saved=%+v err=%v", saved, err)
	}
}

func TestLegacyCAPTCHABanDoneDoesNotPurgeUndeletedHistory(t *testing.T) {
	t.Parallel()
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_rights", true: "rights_restored"}[available], func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			settings := db.DefaultSettings(-100)
			if err := client.SetSettings(t.Context(), settings); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "undeleted history", SentAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			challenge, err := client.CreateChallenge(t.Context(), &db.Challenge{ChatID: -100, CommChatID: 200, UserID: 200, Status: db.ChallengeStatusRejectPending, ActionPhase: db.ChallengePhaseRejectBanDone, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}})
			if err != nil {
				t.Fatal(err)
			}
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				switch method {
				case testTelegramMethodSendMessage:
					return api.Message{MessageID: 90, Chat: api.Chat{ID: -100}}
				case "declineChatJoinRequest":
					return true
				default:
					t.Fatalf("unexpected method: %s", method)
					return nil
				}
			})
			gatekeeper := &Gatekeeper{bot: botAPI, s: &testBotService{settings: settings}, store: client, banChecker: &testGatekeeperBanChecker{moderationUnavailable: !available}}
			if err := gatekeeper.processChallengeActionWithoutStats(t.Context(), challenge); err != nil {
				t.Fatal(err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved == nil {
				t.Fatalf("legacy skipped ban purged context: saved=%+v err=%v", saved, err)
			}
			stored, err := client.GetChallengeByChatUser(t.Context(), -100, 200)
			if err != nil || (available && stored != nil) || (!available && (stored == nil || stored.Status != db.ChallengeStatusNoPrivilegesNotice)) {
				t.Fatalf("legacy/no-rights challenge stopped progressing: stored=%+v err=%v", stored, err)
			}
		})
	}
}

func TestExplicitDeletionContextFollowsTelegramOutcome(t *testing.T) {
	t.Parallel()
	for _, message := range []string{"", "Bad Request: message to delete not found", "Bad Request: MESSAGE_ID_INVALID", testTelegramErrorBadGateway} {
		t.Run(message, func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			record := &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "recorded context", SentAt: now, UpdatedAt: now}
			if err := client.UpsertMessageContext(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				if method != testTelegramMethodDeleteMessage {
					t.Fatalf("unexpected method: %s", method)
				}
				if message != "" {
					return &testBotAPIError{code: 400, description: message}
				}
				return true
			})
			err = bot.DeleteChatMessageAndContext(t.Context(), botAPI, client, -100, 10)
			failed := message == testTelegramErrorBadGateway
			if (err != nil) != failed {
				t.Fatalf("delete outcome error=%v failed=%v", err, failed)
			}
			record.UpdatedAt = now.Add(time.Second)
			if err := client.UpsertMessageContext(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || (saved != nil) != failed {
				t.Fatalf("context deletion disagrees with Telegram: saved=%+v err=%v", saved, err)
			}
		})
	}
}

func (*testReactorStore) DeleteAuthorMessageContext(context.Context, int64, db.MessageAuthor) error {
	return nil
}
func (*testGatekeeperStore) DeleteMessageContext(context.Context, int64, int) error { return nil }
func (*testGatekeeperStore) DeleteAuthorMessageContext(context.Context, int64, db.MessageAuthor) error {
	return nil
}
func (*gatekeeperFlowStore) DeleteMessageContext(context.Context, int64, int) error { return nil }
func (*gatekeeperFlowStore) DeleteAuthorMessageContext(context.Context, int64, db.MessageAuthor) error {
	return nil
}

func TestDeletedCAPTCHAReplySnapshotCannotReturnAsContext(t *testing.T) {
	t.Parallel()
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	message := &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 1, Text: "Please solve this CAPTCHA", SentAt: now, UpdatedAt: now}
	if err := client.UpsertMessageContext(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodDeleteMessage {
			t.Fatalf("unexpected method: %s", method)
		}
		return true
	})
	gatekeeper := &Gatekeeper{bot: botAPI, store: client}
	gatekeeper.deleteChallengePrompt(t.Context(), &db.Challenge{ChatID: -100, CommChatID: -100, ChallengeMessageID: 10})
	message.UpdatedAt = now.Add(time.Second)
	if err := client.UpsertMessageContext(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved != nil {
		t.Fatalf("deleted CAPTCHA remained or replayed: saved=%+v err=%v", saved, err)
	}
}

func TestRestrictedAuthorMembershipBookkeepingUsesIsMember(t *testing.T) {
	t.Parallel()
	for _, isMember := range []bool{false, true} {
		t.Run(map[bool]string{false: "departed", true: "current"}[isMember], func(t *testing.T) {
			f := newTrustFixture(t)
			f.reactor.bot = newTestBotAPI(t, func(method string, _ *http.Request) any {
				if method != testTelegramMethodGetChatMember {
					t.Fatalf("unexpected method: %s", method)
				}
				return api.ChatMember{User: f.user, Status: "restricted", IsMember: isMember}
			})
			if err := f.reactor.rememberAuthorIfPossible(t.Context(), f.chat, f.user, f.reactor.getLogEntry()); err != nil {
				t.Fatal(err)
			}
			knownNonMember, err := f.store.IsChatKnownNonMember(t.Context(), f.chat.ID, f.user.ID)
			if err != nil || knownNonMember == isMember || (f.service.insertedMember == 1) != isMember {
				t.Fatalf("restricted membership=%v remembered incorrectly: nonMember=%v inserts=%d err=%v", isMember, knownNonMember, f.service.insertedMember, err)
			}
		})
	}
}
