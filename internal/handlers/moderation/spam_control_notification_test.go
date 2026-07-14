package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestCreateInChatNotificationRepliesToOriginalMessageWithCappedQuote(t *testing.T) {
	t.Parallel()

	sc := &SpamControl{}
	longText := strings.Repeat("ю", maxReplyQuoteRunes+20)
	msg := &api.Message{
		MessageID:       40,
		MessageThreadID: 7,
		Chat:            api.Chat{ID: -100, Type: moderationTestSupergroup},
		From:            &api.User{ID: 200, FirstName: moderationTestTargetName},
		Text:            longText,
	}

	chattable := sc.createInChatNotification(msg, 1, "en", true)
	reply, ok := chattable.(api.MessageConfig)
	if !ok {
		t.Fatalf("unexpected notification type: %T", chattable)
	}

	if reply.ReplyParameters.MessageID != msg.MessageID {
		t.Fatalf("reply message id = %d, want %d", reply.ReplyParameters.MessageID, msg.MessageID)
	}
	if reply.ReplyParameters.ChatID != msg.Chat.ID {
		t.Fatalf("reply chat id = %v, want %d", reply.ReplyParameters.ChatID, msg.Chat.ID)
	}
	if !reply.ReplyParameters.AllowSendingWithoutReply {
		t.Fatal("expected AllowSendingWithoutReply")
	}
	if reply.MessageThreadID != msg.MessageThreadID {
		t.Fatalf("message thread id = %d, want %d", reply.MessageThreadID, msg.MessageThreadID)
	}
	if utf8.RuneCountInString(reply.ReplyParameters.Quote) != maxReplyQuoteRunes {
		t.Fatalf("quote length = %d, want %d", utf8.RuneCountInString(reply.ReplyParameters.Quote), maxReplyQuoteRunes)
	}
	if !strings.HasPrefix(longText, reply.ReplyParameters.Quote) {
		t.Fatal("expected quote to be an exact prefix of original message text")
	}
}

func TestCreateChannelNotificationRepliesToOriginalCaption(t *testing.T) {
	t.Parallel()

	sc := &SpamControl{}
	msg := &api.Message{
		MessageID:       41,
		MessageThreadID: 8,
		Chat:            api.Chat{ID: -100, Type: moderationTestSupergroup},
		From:            &api.User{ID: 200, FirstName: moderationTestTargetName},
		Caption:         "caption spam",
	}

	chattable := sc.createChannelNotification(msg, "https://t.me/log/1", "en")
	reply, ok := chattable.(api.MessageConfig)
	if !ok {
		t.Fatalf("unexpected notification type: %T", chattable)
	}
	if reply.ReplyParameters.MessageID != msg.MessageID {
		t.Fatalf("reply message id = %d, want %d", reply.ReplyParameters.MessageID, msg.MessageID)
	}
	if reply.ReplyParameters.Quote != msg.Caption {
		t.Fatalf("quote = %q, want %q", reply.ReplyParameters.Quote, msg.Caption)
	}
	if reply.MessageThreadID != msg.MessageThreadID {
		t.Fatalf("message thread id = %d, want %d", reply.MessageThreadID, msg.MessageThreadID)
	}
}

func TestProcessSpamMessageSendsNotificationBeforeDeleteAndRetriesWithoutRejectedQuote(t *testing.T) {
	t.Parallel()

	var methods []string
	var replyParameters []api.ReplyParameters
	botAPI := newModerationRetryTestBotAPI(t, func(method string, r *http.Request) testAPIResponse {
		methods = append(methods, method)
		switch method {
		case testTelegramMethodSendMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			var params api.ReplyParameters
			if err := json.Unmarshal([]byte(r.Form.Get("reply_parameters")), &params); err != nil {
				t.Fatalf("unmarshal reply parameters: %v", err)
			}
			replyParameters = append(replyParameters, params)
			if len(replyParameters) == 1 {
				return testAPIResponse{OK: false, Description: "Bad Request: quote not found"}
			}
			return testAPIResponse{
				OK: true,
				Result: map[string]any{
					moderationTestJSONMessageID: 700,
					moderationTestJSONDate:      0,
					moderationTestJSONChat: map[string]any{
						"id":                   -100,
						moderationTestJSONType: moderationTestSupergroup,
					},
				},
			}
		case testTelegramMethodDeleteMessage:
			return testAPIResponse{OK: true, Result: true}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return testAPIResponse{OK: true, Result: nil}
		}
	})

	store := &testModerationStore{}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Hour},
		banService: &testModerationBanService{},
	}
	msg := &api.Message{
		MessageID:       40,
		MessageThreadID: 7,
		Chat:            api.Chat{ID: -100, Type: moderationTestSupergroup},
		From:            &api.User{ID: 200, FirstName: moderationTestTargetName},
		Text:            "spam text",
	}

	if _, err := sc.ProcessSpamMessage(context.Background(), msg, &msg.Chat, "en"); err != nil {
		t.Fatalf("ProcessSpamMessage returned error: %v", err)
	}

	if len(methods) < 3 {
		t.Fatalf("expected send, retry, and delete methods, got %v", methods)
	}
	if methods[0] != testTelegramMethodSendMessage || methods[1] != testTelegramMethodSendMessage || methods[2] != testTelegramMethodDeleteMessage {
		t.Fatalf("unexpected method order: %v", methods)
	}
	if len(replyParameters) != 2 {
		t.Fatalf("reply parameter attempts = %d, want 2", len(replyParameters))
	}
	if replyParameters[0].MessageID != msg.MessageID || replyParameters[0].Quote != msg.Text {
		t.Fatalf("unexpected first reply parameters: %#v", replyParameters[0])
	}
	if replyParameters[1].MessageID != msg.MessageID || replyParameters[1].Quote != "" {
		t.Fatalf("expected retry to keep reply target but clear quote, got %#v", replyParameters[1])
	}
	if store.spamCase == nil || store.spamCase.NotificationMessageID != 700 {
		t.Fatalf("expected notification id to be stored, got %#v", store.spamCase)
	}
}

func TestVotingSurfaceFallbackPrecedesDestructiveModeration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name             string
		fallbackSucceeds bool
		wantError        bool
		wantMuteCalls    int
		wantDeletes      int
	}{
		{name: "in-chat fallback succeeds", fallbackSucceeds: true, wantMuteCalls: 1, wantDeletes: 1},
		{name: "all voting surfaces fail", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			deleteCalls := 0
			botAPI := newModerationRetryTestBotAPI(t, func(method string, r *http.Request) testAPIResponse {
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse form: %v", err)
				}
				switch method {
				case testTelegramMethodSendMessage:
					if strings.HasPrefix(r.Form.Get("chat_id"), "@") || !test.fallbackSucceeds {
						return testAPIResponse{OK: false, Description: "Bad Request: voting surface unavailable"}
					}
					return testAPIResponse{OK: true, Result: map[string]any{
						moderationTestJSONMessageID: 701,
						moderationTestJSONDate:      0,
						moderationTestJSONChat:      map[string]any{"id": -100, moderationTestJSONType: moderationTestSupergroup},
					}}
				case testTelegramMethodDeleteMessage:
					deleteCalls++
					return testAPIResponse{OK: true, Result: true}
				default:
					t.Fatalf("unexpected bot method: %s", method)
					return testAPIResponse{OK: true}
				}
			})
			store := &testModerationStore{}
			banService := &testModerationBanService{}
			sc := &SpamControl{
				s:          &testModerationService{botAPI: botAPI},
				bot:        botAPI,
				store:      store,
				config:     config.SpamControl{LogChannelUsername: "log_channel", VotingTimeoutMinutes: time.Minute},
				banService: banService,
			}
			msg := &api.Message{
				MessageID: 40,
				Chat:      api.Chat{ID: -100, Type: moderationTestSupergroup},
				From:      &api.User{ID: 200, FirstName: moderationTestTargetName},
				Text:      "candidate",
			}

			_, err := sc.ProcessSpamMessage(context.Background(), msg, &msg.Chat, "en")
			if (err != nil) != test.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if banService.muteCalls != test.wantMuteCalls {
				t.Fatalf("mute calls = %d, want %d", banService.muteCalls, test.wantMuteCalls)
			}
			if deleteCalls != test.wantDeletes {
				t.Fatalf("delete calls = %d, want %d", deleteCalls, test.wantDeletes)
			}
			if test.fallbackSucceeds && (store.spamCase == nil || store.spamCase.NotificationMessageID != 701 || store.spamCase.ChannelPostID != 0) {
				t.Fatalf("expected in-chat fallback presentation, got %#v", store.spamCase)
			}
		})
	}
}

func TestVotingPermissionFailurePreservesOriginalMessage(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	nextMessageID := 700
	botAPI := newModerationRetryTestBotAPI(t, func(method string, _ *http.Request) testAPIResponse {
		switch method {
		case testTelegramMethodSendMessage:
			nextMessageID++
			return testAPIResponse{OK: true, Result: map[string]any{
				moderationTestJSONMessageID: nextMessageID,
				moderationTestJSONDate:      0,
				moderationTestJSONChat:      map[string]any{"id": -100, moderationTestJSONType: moderationTestSupergroup},
			}}
		case testTelegramMethodDeleteMessage:
			deleteCalls++
			return testAPIResponse{OK: true, Result: true}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return testAPIResponse{OK: true}
		}
	})
	store := &testModerationStore{}
	banService := &testModerationBanService{muteErr: ErrNoPrivileges}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Hour, VotingTimeoutMinutes: time.Minute},
		banService: banService,
	}
	msg := &api.Message{
		MessageID: 40,
		Chat:      api.Chat{ID: -100, Type: moderationTestSupergroup},
		From:      &api.User{ID: 200, FirstName: moderationTestTargetName},
		Text:      "candidate",
	}

	result, err := sc.ProcessSpamMessage(context.Background(), msg, &msg.Chat, "en")
	if err != nil {
		t.Fatalf("ProcessSpamMessage returned error: %v", err)
	}
	if result.Error != errChatAdminRequired || result.MessageDeleted || result.UserBanned {
		t.Fatalf("unexpected processing result: %#v", result)
	}
	if deleteCalls != 0 {
		t.Fatalf("original message was deleted after mute failure: %d delete calls", deleteCalls)
	}
	if store.spamCase == nil || store.spamCase.PreVoteRestricted {
		t.Fatalf("failed mute was persisted as an applied restriction: %#v", store.spamCase)
	}
}

func TestKnownBanPermissionFailurePreservesOriginalMessage(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	nextMessageID := 700
	botAPI := newModerationRetryTestBotAPI(t, func(method string, _ *http.Request) testAPIResponse {
		switch method {
		case testTelegramMethodSendMessage:
			nextMessageID++
			return testAPIResponse{OK: true, Result: map[string]any{
				moderationTestJSONMessageID: nextMessageID,
				moderationTestJSONDate:      0,
				moderationTestJSONChat:      map[string]any{"id": -100, moderationTestJSONType: moderationTestSupergroup},
			}}
		case testTelegramMethodBanChatMember:
			return testAPIResponse{OK: false, Description: "Bad Request: not enough rights to restrict/unrestrict chat member"}
		case testTelegramMethodDeleteMessage:
			deleteCalls++
			return testAPIResponse{OK: true, Result: true}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return testAPIResponse{OK: true}
		}
	})
	store := &testModerationStore{}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Hour},
		banService: &testModerationBanService{},
	}
	msg := &api.Message{
		MessageID: 40,
		Chat:      api.Chat{ID: -100, Type: moderationTestSupergroup},
		From:      &api.User{ID: 200, FirstName: moderationTestTargetName},
		Text:      "known spammer",
	}

	result, err := sc.ProcessBannedMessage(context.Background(), msg, &msg.Chat, "en")
	if err != nil {
		t.Fatalf("ProcessBannedMessage returned error: %v", err)
	}
	if result.Error != errChatAdminRequired || result.MessageDeleted || result.UserBanned {
		t.Fatalf("unexpected processing result: %#v", result)
	}
	if deleteCalls != 0 {
		t.Fatalf("original message was deleted after ban failure: %d delete calls", deleteCalls)
	}
	if len(store.deletedKnownNonMember) != 0 {
		t.Fatalf("known non-member state was cleared after ban failure: %#v", store.deletedKnownNonMember)
	}
	if store.spamCase == nil || store.spamCase.Status != db.SpamCaseStatusResolvingSpam {
		t.Fatalf("failed known ban was not retained as durable resolving work: %#v", store.spamCase)
	}
	if store.retryCalls != 1 || !store.spamCase.NextAttemptAt.Valid || store.spamCase.AttemptCount != 1 {
		t.Fatalf("failed known ban did not schedule a retry: case=%#v retry_calls=%d", store.spamCase, store.retryCalls)
	}
}

func TestAbsentUserUnmuteIsTreatedAsAlreadyApplied(t *testing.T) {
	t.Parallel()

	spamCase := &db.SpamCase{
		ID:                1,
		ChatID:            -100,
		UserID:            200,
		MessageID:         40,
		PreVoteRestricted: true,
		Status:            db.SpamCaseStatusResolvingFalsePositive,
	}
	store := &testModerationStore{spamCase: spamCase}
	banService := &testModerationBanService{unmuteErr: errors.New("Bad Request: PARTICIPANT_ID_INVALID")}
	sc := &SpamControl{store: store, banService: banService}

	if err := sc.resolveClaimedCase(context.Background(), spamCase); err != nil {
		t.Fatalf("resolveClaimedCase returned error: %v", err)
	}
	if store.retryCalls != 0 {
		t.Fatalf("retry scheduled for already-absent user: %d", store.retryCalls)
	}
	if store.spamCase.Status != db.SpamCaseStatusFalsePositive || store.spamCase.ResolvedAt == nil {
		t.Fatalf("case was not finalized: %#v", store.spamCase)
	}
}

func TestVotingSurfacePersistenceFailureCompensatesBeforeModeration(t *testing.T) {
	t.Parallel()

	deletedMessageIDs := make([]string, 0, 1)
	botAPI := newModerationRetryTestBotAPI(t, func(method string, r *http.Request) testAPIResponse {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch method {
		case testTelegramMethodSendMessage:
			return testAPIResponse{OK: true, Result: map[string]any{
				moderationTestJSONMessageID: 701,
				moderationTestJSONDate:      0,
				moderationTestJSONChat:      map[string]any{"id": -100, moderationTestJSONType: moderationTestSupergroup},
			}}
		case testTelegramMethodDeleteMessage:
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get("message_id"))
			return testAPIResponse{OK: true, Result: true}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return testAPIResponse{OK: true}
		}
	})
	store := &testModerationStore{presentationErr: errors.New("database unavailable")}
	banService := &testModerationBanService{}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{VotingTimeoutMinutes: time.Minute},
		banService: banService,
	}
	msg := &api.Message{
		MessageID: 40,
		Chat:      api.Chat{ID: -100, Type: moderationTestSupergroup},
		From:      &api.User{ID: 200, FirstName: moderationTestTargetName},
		Text:      "candidate",
	}

	if _, err := sc.ProcessSpamMessage(context.Background(), msg, &msg.Chat, "en"); err == nil {
		t.Fatal("expected voting surface persistence failure")
	}
	if banService.muteCalls != 0 {
		t.Fatalf("mute started before durable voting surface: %d calls", banService.muteCalls)
	}
	if len(deletedMessageIDs) != 1 || deletedMessageIDs[0] != "701" {
		t.Fatalf("expected only voting prompt compensation, got deleted message ids %v", deletedMessageIDs)
	}
}

type testAPIResponse struct {
	OK          bool
	Result      any
	Description string
}

func newModerationRetryTestBotAPI(t *testing.T, handler func(method string, r *http.Request) testAPIResponse) *api.BotAPI {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()

		method := path.Base(r.URL.Path)
		response := testAPIResponse{OK: true}
		switch method {
		case "getMe":
			response.Result = map[string]any{
				"id":                        1,
				moderationTestJSONIsBot:     true,
				moderationTestJSONFirstName: "Test",
				"username":                  "testbot",
			}
		default:
			response = handler(method, r)
		}

		payload := map[string]any{
			"ok": response.OK,
		}
		if response.Description != "" {
			payload["description"] = response.Description
		}
		if response.Result != nil {
			payload["result"] = response.Result
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	botAPI, err := api.NewBotAPIWithOptions(
		"TEST_TOKEN",
		api.WithAPIEndpoint(fmt.Sprintf("%s/bot%%s/%%s", server.URL)),
		api.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("new test bot api: %v", err)
	}
	return botAPI
}
