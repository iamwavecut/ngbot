package handlers

import (
	"context"
	"encoding/json"
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
)

func TestCreateInChatNotificationRepliesToOriginalMessageWithCappedQuote(t *testing.T) {
	t.Parallel()

	sc := &SpamControl{}
	longText := strings.Repeat("ю", maxReplyQuoteRunes+20)
	msg := &api.Message{
		MessageID:       40,
		MessageThreadID: 7,
		Chat:            api.Chat{ID: -100, Type: "supergroup"},
		From:            &api.User{ID: 200, FirstName: "Target"},
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
		Chat:            api.Chat{ID: -100, Type: "supergroup"},
		From:            &api.User{ID: 200, FirstName: "Target"},
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
					"message_id": 700,
					"date":       0,
					"chat": map[string]any{
						"id":   -100,
						"type": "supergroup",
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
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Hour},
		banService: &testModerationBanService{},
	}
	msg := &api.Message{
		MessageID:       40,
		MessageThreadID: 7,
		Chat:            api.Chat{ID: -100, Type: "supergroup"},
		From:            &api.User{ID: 200, FirstName: "Target"},
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
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
				"username":   "testbot",
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

	botAPI, err := api.NewBotAPIWithClient("TEST_TOKEN", fmt.Sprintf("%s/bot%%s/%%s", server.URL), server.Client())
	if err != nil {
		t.Fatalf("new test bot api: %v", err)
	}
	return botAPI
}
