package handlers

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
)

const (
	testTelegramMethodBanChatSenderChat = "banChatSenderChat"
	moderationTestChannelTitle          = "Channel"
)

func TestSenderChatSuspicionDeletesWithoutMutingAndSuspendsTrust(t *testing.T) {
	t.Parallel()

	for _, fakeUser := range []*api.User{nil, {ID: 777000, FirstName: "Technical sender"}} {
		t.Run(strings.ReplaceAll(fakeUserName(fakeUser), " ", "_"), func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "cases.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
				t.Fatal(err)
			}
			author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
			now := time.Now().UTC()
			if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 10, now, 1, time.Hour, true); err != nil {
				t.Fatal(err)
			}
			contextRecord := &db.MessageContext{ChatID: -100, MessageID: 40, AuthorKind: author.Kind, AuthorID: author.ID, Text: moderationTestCandidateText, SentAt: now, UpdatedAt: now}
			if err := client.UpsertMessageContext(t.Context(), contextRecord); err != nil {
				t.Fatal(err)
			}
			var methods []string
			botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
				methods = append(methods, method)
				switch method {
				case testTelegramMethodSendMessage:
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					text := r.Form.Get("text")
					if !strings.Contains(text, "Channel title") || strings.Contains(text, "Technical sender") || strings.Contains(text, "tg://user") {
						t.Errorf("notification used wrong identity: %q", text)
					}
					return api.Message{MessageID: 700, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}}
				case testTelegramMethodDeleteMessage:
					return true
				default:
					t.Errorf("unexpected Telegram call before a vote: %s", method)
					return true
				}
			})
			banService := &testModerationBanService{}
			control := &SpamControl{s: &testModerationService{}, bot: botAPI, store: client, banService: banService, config: config.SpamControl{VotingTimeoutMinutes: time.Hour}}
			message := &api.Message{MessageID: 40, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}, SenderChat: &api.Chat{ID: -300, Title: "Channel title", Type: "channel"}, From: fakeUser, Text: moderationTestCandidateText}
			result, err := control.ProcessSpamMessage(t.Context(), message, &message.Chat, "en")
			if err != nil {
				t.Fatal(err)
			}
			if !result.MessageDeleted || result.UserBanned || banService.muteCalls != 0 || !slices.Equal(methods, []string{testTelegramMethodSendMessage, testTelegramMethodDeleteMessage}) {
				t.Fatalf("channel was not held for voting: result=%+v muteCalls=%d methods=%v", result, banService.muteCalls, methods)
			}
			cases, err := client.GetPendingSpamCases(t.Context())
			if err != nil || len(cases) != 1 || cases[0].UserID != -300 || cases[0].ResolveAt == nil || cases[0].PreVoteRestricted {
				t.Fatalf("channel case not persisted: cases=%+v err=%v", cases, err)
			}
			trust, err := client.MessageTrust(t.Context(), -100, author)
			if err != nil || trust == nil || !trust.Suspended || trust.Trusted(now) || trust.SafeMessages != 1 {
				t.Fatalf("channel trust not suspended: trust=%+v err=%v", trust, err)
			}
			contextRecord.UpdatedAt = now.Add(time.Second)
			if err := client.UpsertMessageContext(t.Context(), contextRecord); err != nil {
				t.Fatal(err)
			}
			if record, err := client.MessageContext(t.Context(), -100, 40); err != nil || record != nil {
				t.Fatalf("deleted message context was resurrected: record=%+v err=%v", record, err)
			}
		})
	}
}

func TestSenderChatReportRequiresMemberVoteAndBansOnlyChannel(t *testing.T) {
	t.Parallel()

	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "cases.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 10, now, 1, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []int{40, 50} {
		if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: messageID, AuthorKind: author.Kind, AuthorID: author.ID, Text: "recorded message", SentAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	var methods, deleted []string
	memberStatus := moderationTestMemberStatusLeft
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, method)
		switch method {
		case testTelegramMethodSendMessage:
			return api.Message{MessageID: 700, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}}
		case moderationTestTelegramMethodGetChatMember:
			if r.Form.Get("chat_id") != "-100" || r.Form.Get("user_id") != "777000" {
				t.Errorf("voter membership checked wrong identity: %v", r.Form)
			}
			return api.ChatMember{User: &api.User{ID: 777000}, Status: memberStatus}
		case testTelegramMethodBanChatSenderChat:
			if r.Form.Get("chat_id") != "-100" || r.Form.Get("sender_chat_id") != "-300" || r.Form.Get("user_id") != "" {
				t.Errorf("channel ban used wrong identity: %v", r.Form)
			}
			return true
		case testTelegramMethodDeleteMessage:
			deleted = append(deleted, r.Form.Get("message_id"))
			return true
		default:
			t.Errorf("unexpected Telegram method %s", method)
			return true
		}
	})
	banService := &testModerationBanService{}
	control := &SpamControl{s: &testModerationService{}, bot: botAPI, store: client, banService: banService, config: config.SpamControl{MinVoters: 1}}
	target := &api.Message{MessageID: 40, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}, SenderChat: &api.Chat{ID: -300, Title: moderationTestChannelTitle}, Text: "reported candidate"}
	report := &api.Message{MessageID: 50, Chat: target.Chat, From: &api.User{ID: 777000}, Text: "/spam"}
	for range 2 {
		if _, err := control.ProcessReportedMessage(t.Context(), target, report, &target.Chat, "en"); err != nil {
			t.Fatal(err)
		}
	}
	cases, err := client.GetPendingSpamCases(t.Context())
	if err != nil || len(cases) != 1 || cases[0].Author() != author || cases[0].PreVoteRestricted {
		t.Fatalf("channel report case was not persisted once: cases=%+v err=%v", cases, err)
	}
	if !slices.Equal(methods, []string{testTelegramMethodSendMessage}) || banService.muteCalls != 0 {
		t.Fatalf("report enforced without voting: methods=%v mutes=%d", methods, banService.muteCalls)
	}
	caseID := cases[0].ID
	if _, _, err := control.RecordVote(t.Context(), caseID, 777000, "", false); !errors.Is(err, ErrVoterNotEligible) {
		t.Fatalf("departed reporter voted: %v", err)
	}
	memberStatus = "restricted"
	if _, _, err := control.RecordVote(t.Context(), caseID, 777000, "", false); !errors.Is(err, ErrVoterNotEligible) {
		t.Fatalf("restricted non-member reporter voted: %v", err)
	}
	memberStatus = moderationTestMemberStatusMember
	if _, _, err := control.RecordVote(t.Context(), caseID, 777000, "", false); err != nil {
		t.Fatal(err)
	}
	resolved, err := client.GetSpamCase(t.Context(), caseID)
	if err != nil || resolved.Status != db.SpamCaseStatusSpam || !slices.Contains(methods, testTelegramMethodBanChatSenderChat) || !slices.Contains(deleted, "40") {
		t.Fatalf("channel vote not enforced: case=%+v methods=%v deleted=%v err=%v", resolved, methods, deleted, err)
	}
	trust, err := client.MessageTrust(t.Context(), -100, author)
	if err != nil || trust == nil || trust.Suspended || trust.SafeMessages != 0 || trust.TrustedUntil.Valid {
		t.Fatalf("confirmed channel retained trust: %+v err=%v", trust, err)
	}
	control.cleanupDueReportMessages(t.Context(), time.Now().Add(time.Hour))
	for _, messageID := range []int{40, 50} {
		if record, err := client.MessageContext(t.Context(), -100, messageID); err != nil || record != nil {
			t.Fatalf("deleted message %d context remained: record=%+v err=%v", messageID, record, err)
		}
	}
}

func TestSenderChatKnownSpamEnforcesWithoutVote(t *testing.T) {
	t.Parallel()

	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "cases.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	var methods []string
	botAPI := newModerationTestBotAPI(t, func(method string, _ *http.Request) any {
		methods = append(methods, method)
		if method == testTelegramMethodSendMessage {
			return api.Message{MessageID: 700, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}}
		}
		if method != testTelegramMethodBanChatSenderChat && method != testTelegramMethodDeleteMessage {
			t.Errorf("wrong enforcement method %s", method)
		}
		return true
	})
	banService := &testModerationBanService{}
	control := &SpamControl{s: &testModerationService{}, bot: botAPI, store: client, banService: banService, config: config.SpamControl{SuspectNotificationTimeout: time.Hour}, runtimeCtx: t.Context()}
	message := &api.Message{MessageID: 40, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}, SenderChat: &api.Chat{ID: -300, Title: moderationTestChannelTitle}, From: &api.User{ID: 777000}, Text: moderationTestCandidateText}
	result, err := control.ProcessBannedMessage(t.Context(), message, &message.Chat, "en")
	if err != nil || !result.MessageDeleted || !result.UserBanned || banService.muteCalls != 0 || !slices.Equal(methods, []string{testTelegramMethodSendMessage, testTelegramMethodBanChatSenderChat, testTelegramMethodDeleteMessage}) {
		t.Fatalf("immediate channel enforcement failed: result=%+v methods=%v err=%v", result, methods, err)
	}
}

func TestSenderChatResolutionRecoversPendingCaseAndTransientBanAfterRestart(t *testing.T) {
	t.Parallel()

	for _, voting := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "voting"}[voting], func(t *testing.T) {
			dir := t.TempDir()
			client, err := sqlite.NewSQLiteClient(t.Context(), dir, "cases.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
			technicalUser := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 777000}
			for i, identity := range []db.MessageAuthor{author, technicalUser} {
				if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, identity, i+1, now, 1, time.Hour, true); err != nil {
					t.Fatal(err)
				}
			}
			pending := &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 40, CreatedAt: now, Status: db.SpamCaseStatusPending}
			if voting {
				deadline := now.Add(-time.Minute)
				pending.ResolveAt = &deadline
			}
			spamCase, err := client.CreateSpamCase(t.Context(), pending)
			if err != nil {
				t.Fatal(err)
			}
			if voting {
				if _, _, _, err := client.AddVoteIfPending(t.Context(), &db.SpamVote{CaseID: spamCase.ID, VoterID: 200, VotedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client, err = sqlite.NewSQLiteClient(t.Context(), dir, "cases.db")
			if err != nil {
				t.Fatal(err)
			}
			banAttempts := 0
			botAPI := newModerationRetryTestBotAPI(t, func(method string, r *http.Request) testAPIResponse {
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				switch method {
				case testTelegramMethodBanChatSenderChat:
					banAttempts++
					if r.Form.Get("sender_chat_id") != "-300" || r.Form.Get("chat_id") != "-100" {
						t.Errorf("recovered ban changed target: %v", r.Form)
					}
					if banAttempts == 1 {
						return testAPIResponse{OK: false, Description: moderationTestErrorBadGateway}
					}
				case testTelegramMethodDeleteMessage:
					if r.Form.Get("message_id") != "40" {
						t.Errorf("recovered deletion changed target: %v", r.Form)
					}
				default:
					t.Errorf("unexpected recovered Telegram action: %s", method)
				}
				return testAPIResponse{OK: true, Result: true}
			})
			banService := &testModerationBanService{}
			control := &SpamControl{s: &testModerationService{}, bot: botAPI, store: client, banService: banService, config: config.SpamControl{MinVoters: 1}}
			recoverErr := control.recoverPendingSpamCaseDeadlines(t.Context())
			if voting && recoverErr != nil {
				t.Fatal(recoverErr)
			}
			control.processDurableWork(t.Context())
			stored, err := client.GetSpamCase(t.Context(), spamCase.ID)
			if err != nil || stored.Author() != author || stored.Status != db.SpamCaseStatusResolvingSpam || stored.AttemptCount != 1 || !stored.NextAttemptAt.Valid || banAttempts != 1 {
				t.Fatalf("transient channel resolution was not retained: case=%+v attempts=%d err=%v", stored, banAttempts, err)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client, err = sqlite.NewSQLiteClient(t.Context(), dir, "cases.db")
			if err != nil {
				t.Fatal(err)
			}
			control.store = client
			due, err := client.GetDueSpamCases(t.Context(), stored.NextAttemptAt.Time.Add(time.Second))
			if err != nil || len(due) != 1 || due[0].ID != spamCase.ID || due[0].Author() != author {
				t.Fatalf("retry lost its channel identity: due=%+v err=%v", due, err)
			}
			if err := control.resolveClaimedCase(t.Context(), due[0]); err != nil {
				t.Fatal(err)
			}
			stored, err = client.GetSpamCase(t.Context(), spamCase.ID)
			if err != nil || stored.Status != db.SpamCaseStatusSpam || stored.NextAttemptAt.Valid || banAttempts != 2 || banService.muteCalls != 0 || banService.unmuteCalls != 0 {
				t.Fatalf("recovered channel resolution failed: case=%+v attempts=%d err=%v", stored, banAttempts, err)
			}
			for _, identity := range []db.MessageAuthor{author, technicalUser} {
				trust, err := client.MessageTrust(t.Context(), -100, identity)
				if err != nil || trust == nil || trust.Suspended || trust.Trusted(now) != (identity == technicalUser) {
					t.Fatalf("resolution changed wrong author trust: author=%+v trust=%+v err=%v", identity, trust, err)
				}
			}
		})
	}
}

func TestResolvingSenderChatForegroundReplayRetainsOneCaseAndVotingSurface(t *testing.T) {
	t.Parallel()

	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "cases.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	author := db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -300}
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, time.Now(), 1, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	var methods []string
	allowBan := false
	botAPI := newModerationRetryTestBotAPI(t, func(method string, _ *http.Request) testAPIResponse {
		methods = append(methods, method)
		switch method {
		case testTelegramMethodSendMessage:
			return testAPIResponse{OK: true, Result: api.Message{MessageID: 700, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}}}
		case testTelegramMethodBanChatSenderChat:
			if !allowBan {
				return testAPIResponse{OK: false, Description: moderationTestErrorBadGateway}
			}
		case testTelegramMethodDeleteMessage:
		default:
			t.Errorf("unexpected replay action: %s", method)
		}
		return testAPIResponse{OK: true, Result: true}
	})
	control := &SpamControl{s: &testModerationService{}, bot: botAPI, store: client, banService: &testModerationBanService{}, runtimeCtx: t.Context(), config: config.SpamControl{SuspectNotificationTimeout: time.Hour}}
	message := &api.Message{MessageID: 40, Chat: api.Chat{ID: -100, Type: moderationTestSupergroup}, SenderChat: &api.Chat{ID: -300, Title: moderationTestChannelTitle}, Text: moderationTestCandidateText}
	if _, err := control.ProcessBannedMessage(t.Context(), message, &message.Chat, "en"); err == nil {
		t.Fatal("expected initial transient ban failure")
	}
	initial, err := client.GetDueSpamCases(t.Context(), time.Now().Add(time.Minute))
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial case missing: cases=%+v err=%v", initial, err)
	}
	if _, err := control.ProcessBannedMessage(t.Context(), message, &message.Chat, "en"); err != nil {
		t.Errorf("foreground replay did not hand off to durable resolution: %v", err)
	}
	report := &api.Message{MessageID: 90, Chat: message.Chat, From: &api.User{ID: 200}, Text: "/spam"}
	if _, err := control.ProcessReportedMessage(t.Context(), message, report, &message.Chat, "en"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(methods, []string{testTelegramMethodSendMessage, testTelegramMethodBanChatSenderChat}) {
		t.Fatalf("foreground replay duplicated surface or enforcement: %v", methods)
	}
	due, err := client.GetDueSpamCases(t.Context(), time.Now().Add(time.Minute))
	if err != nil || len(due) != 1 || due[0].ID != initial[0].ID || due[0].AttemptCount != 1 {
		t.Fatalf("foreground replay duplicated resolution: cases=%+v err=%v", due, err)
	}
	allowBan = true
	if err := control.resolveClaimedCase(t.Context(), due[0]); err != nil {
		t.Fatal(err)
	}
	control.processDurableWork(t.Context())
	if !slices.Equal(methods, []string{testTelegramMethodSendMessage, testTelegramMethodBanChatSenderChat, testTelegramMethodBanChatSenderChat, testTelegramMethodDeleteMessage}) {
		t.Fatalf("durable resolution repeated effects: %v", methods)
	}
	stat, err := client.GetKV(t.Context(), handlersbase.StatsKey(-100, time.Now(), handlersbase.StatSpamConfirmed))
	if err != nil || stat != "1" {
		t.Fatalf("confirmation counted more than once: stat=%q err=%v", stat, err)
	}
	if trust, err := client.MessageTrust(t.Context(), -100, author); err != nil || trust == nil || trust.Suspended || trust.SafeMessages != 0 || trust.TrustedUntil.Valid {
		t.Fatalf("replayed case corrupted reset trust: trust=%+v err=%v", trust, err)
	}
}

func fakeUserName(user *api.User) string {
	if user == nil {
		return "without fake From"
	}
	return "with fake From"
}
