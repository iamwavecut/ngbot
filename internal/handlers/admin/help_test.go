package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

const (
	adminTestUserName            = "User"
	adminTestMethodSendMessage   = "sendMessage"
	adminTestMethodGetChatMember = "getChatMember"
	adminTestJSONType            = "type"
	adminTestJSONFirstName       = "first_name"
	adminTestJSONIsBot           = "is_bot"
)

type adminTelegramCall struct {
	method string
	form   url.Values
}

func TestPrivateHelpCommandsSendLocalizedMarkdownHelp(t *testing.T) {
	i18n.Init()
	ctx := t.Context()
	client := newAdminTestDB(t, ctx)
	settings := db.DefaultSettings(123)
	settings.Language = "ru"
	if err := client.SetSettings(ctx, settings); err != nil {
		t.Fatalf("set private settings: %v", err)
	}

	var mu sync.Mutex
	var calls []adminTelegramCall
	botAPI := newAdminTestBotAPI(t, func(method string, r *http.Request) any {
		form := cloneForm(t, r)
		mu.Lock()
		calls = append(calls, adminTelegramCall{method: method, form: form})
		mu.Unlock()
		switch method {
		case adminTestMethodSendMessage:
			return sentMessageResult(form)
		default:
			t.Fatalf("unexpected telegram method: %s", method)
			return nil
		}
	})

	admin := NewAdmin(testAdminService{db: client, bot: botAPI}, botAPI, client, client, nil)
	for _, text := range []string{"/help", "/start", "/start help", "/start unknown"} {
		msg := adminCommandMessage(123, panelChatTypePrivate, 77, text)
		proceed, err := admin.Handle(ctx, &api.Update{Message: msg}, &msg.Chat, msg.From)
		if err != nil {
			t.Fatalf("handle %q: %v", text, err)
		}
		if proceed {
			t.Fatalf("expected %q to stop handler chain", text)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("sendMessage calls = %d, want 4", len(calls))
	}
	firstText := calls[0].form.Get("text")
	for _, call := range calls {
		if call.method != adminTestMethodSendMessage {
			t.Fatalf("unexpected method: %s", call.method)
		}
		if call.form.Get("parse_mode") != api.ModeMarkdownV2 {
			t.Fatalf("parse mode = %q, want %q", call.form.Get("parse_mode"), api.ModeMarkdownV2)
		}
		if call.form.Get("text") != firstText {
			t.Fatalf("help text differs for equivalent private help trigger")
		}
	}
	if !strings.Contains(firstText, "*Справка*") {
		t.Fatalf("expected localized markdown help title, got %q", firstText)
	}
	if !strings.Contains(firstText, "`/help`") || !strings.Contains(firstText, "`/voteban`") {
		t.Fatalf("expected markdown code commands in help, got %q", firstText)
	}
}

func TestStartSettingsPayloadStillUsesSettingsFlow(t *testing.T) {
	i18n.Init()
	ctx := t.Context()
	client := newAdminTestDB(t, ctx)
	targetChatID := int64(-100123)
	if err := client.SetChatBotMembership(ctx, &db.ChatBotMembership{ChatID: targetChatID, IsMember: true}); err != nil {
		t.Fatalf("set membership: %v", err)
	}
	if err := client.UpsertChatManager(ctx, &db.ChatManager{ChatID: targetChatID, UserID: 7, CanRestrictMembers: true}); err != nil {
		t.Fatalf("set manager: %v", err)
	}
	if err := client.SetSettings(ctx, db.DefaultSettings(targetChatID)); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	var mu sync.Mutex
	var methods []string
	botAPI := newAdminTestBotAPI(t, func(method string, r *http.Request) any {
		form := cloneForm(t, r)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case adminTestMethodSendMessage:
			return sentMessageResult(form)
		case "sendChatAction":
			return true
		case "editMessageText":
			return map[string]any{
				"message_id": mustFormInt(form.Get("message_id")),
				"chat":       map[string]any{"id": mustFormInt64(form.Get("chat_id")), adminTestJSONType: panelChatTypePrivate},
				"text":       form.Get("text"),
			}
		case adminTestMethodGetChatMember:
			return map[string]any{
				"status":                "administrator",
				telegramProfileHostUser: map[string]any{"id": 7, adminTestJSONIsBot: false, adminTestJSONFirstName: adminTestUserName},
				"can_manage_chat":       true,
				"can_promote_members":   false,
				"can_restrict_members":  true,
			}
		case "getChat":
			return map[string]any{
				"id":              targetChatID,
				adminTestJSONType: panelChatTypeSupergroup,
				"title":           "Target",
			}
		default:
			t.Fatalf("unexpected telegram method: %s", method)
			return nil
		}
	})

	admin := NewAdmin(testAdminService{db: client, bot: botAPI}, botAPI, client, client, nil)
	msg := adminCommandMessage(7, panelChatTypePrivate, 88, "/start settings_"+encodeChatID(targetChatID))
	proceed, err := admin.Handle(ctx, &api.Update{Message: msg}, &msg.Chat, msg.From)
	if err != nil {
		t.Fatalf("handle start settings: %v", err)
	}
	if proceed {
		t.Fatalf("expected settings start to stop handler chain")
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(methods, "editMessageText") {
		t.Fatalf("expected settings flow to edit placeholder, got methods %v", methods)
	}
}

func TestGroupHelpSendsPrivateBridgeAndDeletesMessages(t *testing.T) {
	i18n.Init()
	ctx := t.Context()
	client := newAdminTestDB(t, ctx)
	settings := db.DefaultSettings(-100)
	settings.Language = "ru"
	if err := client.SetSettings(ctx, settings); err != nil {
		t.Fatalf("set group settings: %v", err)
	}

	var mu sync.Mutex
	var calls []adminTelegramCall
	botAPI := newAdminTestBotAPI(t, func(method string, r *http.Request) any {
		form := cloneForm(t, r)
		mu.Lock()
		calls = append(calls, adminTelegramCall{method: method, form: form})
		mu.Unlock()
		switch method {
		case adminTestMethodSendMessage:
			result := sentMessageResult(form)
			result["message_id"] = 700
			return result
		case "deleteMessage":
			return true
		default:
			t.Fatalf("unexpected telegram method: %s", method)
			return nil
		}
	})

	admin := NewAdmin(testAdminService{db: client, bot: botAPI}, botAPI, client, client, nil)
	admin.temporaryMessageTTL = 10 * time.Millisecond
	msg := adminCommandMessage(-100, panelChatTypeSupergroup, 44, "/help")
	proceed, err := admin.Handle(ctx, &api.Update{Message: msg}, &msg.Chat, msg.From)
	if err != nil {
		t.Fatalf("handle group help: %v", err)
	}
	if proceed {
		t.Fatalf("expected group help bridge to stop handler chain")
	}

	eventually(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return countAdminCalls(calls, "deleteMessage") == 2
	})

	mu.Lock()
	defer mu.Unlock()
	send := firstAdminCall(t, calls, adminTestMethodSendMessage)
	if send.form.Get("parse_mode") != api.ModeMarkdownV2 {
		t.Fatalf("parse mode = %q, want %q", send.form.Get("parse_mode"), api.ModeMarkdownV2)
	}
	if !strings.Contains(send.form.Get("reply_markup"), "https://t.me/testbot?start=help") {
		t.Fatalf("expected private help button, got markup %q", send.form.Get("reply_markup"))
	}

	var deleted []string
	for _, call := range calls {
		if call.method == "deleteMessage" {
			deleted = append(deleted, call.form.Get("message_id"))
		}
	}
	slices.Sort(deleted)
	if strings.Join(deleted, ",") != "44,700" {
		t.Fatalf("deleted message ids = %v, want [44 700]", deleted)
	}
}

func TestPrivateLangIsAvailableWithoutAdminCheck(t *testing.T) {
	i18n.Init()
	ctx := t.Context()
	client := newAdminTestDB(t, ctx)

	var mu sync.Mutex
	var methods []string
	botAPI := newAdminTestBotAPI(t, func(method string, r *http.Request) any {
		form := cloneForm(t, r)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case adminTestMethodSendMessage:
			return sentMessageResult(form)
		case adminTestMethodGetChatMember:
			t.Fatalf("private /lang must not call getChatMember")
			return nil
		default:
			t.Fatalf("unexpected telegram method: %s", method)
			return nil
		}
	})

	admin := NewAdmin(testAdminService{db: client, bot: botAPI}, botAPI, client, client, nil)
	msg := adminCommandMessage(123, panelChatTypePrivate, 55, "/lang ru")
	proceed, err := admin.Handle(ctx, &api.Update{Message: msg}, &msg.Chat, msg.From)
	if err != nil {
		t.Fatalf("handle private lang: %v", err)
	}
	if proceed {
		t.Fatalf("expected private lang to stop handler chain")
	}

	got, err := client.GetSettings(ctx, 123)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if got == nil || got.Language != "ru" {
		t.Fatalf("private language = %#v, want ru", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(methods, adminTestMethodSendMessage) {
		t.Fatalf("expected confirmation message, got methods %v", methods)
	}
}

func TestGroupLangRemainsAdminOnly(t *testing.T) {
	i18n.Init()
	ctx := t.Context()
	client := newAdminTestDB(t, ctx)
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("set group settings: %v", err)
	}

	var mu sync.Mutex
	var methods []string
	botAPI := newAdminTestBotAPI(t, func(method string, r *http.Request) any {
		_ = cloneForm(t, r)
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case adminTestMethodGetChatMember:
			return map[string]any{
				"status":                "member",
				telegramProfileHostUser: map[string]any{"id": 7, adminTestJSONIsBot: false, adminTestJSONFirstName: adminTestUserName},
			}
		default:
			t.Fatalf("unexpected telegram method: %s", method)
			return nil
		}
	})

	admin := NewAdmin(testAdminService{db: client, bot: botAPI}, botAPI, client, client, nil)
	msg := adminCommandMessage(-100, "group", 56, "/lang ru")
	proceed, err := admin.Handle(ctx, &api.Update{Message: msg}, &msg.Chat, msg.From)
	if err != nil {
		t.Fatalf("handle group lang: %v", err)
	}
	if !proceed {
		t.Fatalf("expected non-admin group lang to proceed unchanged")
	}

	got, err := client.GetSettings(ctx, -100)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if got != nil && got.Language == "ru" {
		t.Fatalf("non-admin group /lang changed language: %#v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(methods, []string{adminTestMethodGetChatMember}) {
		t.Fatalf("methods = %v, want only getChatMember", methods)
	}
}

type adminTestDB interface {
	adminStore
	handlersbase.StatsStore
	Close() error
	GetSettings(ctx context.Context, chatID int64) (*db.Settings, error)
	SetSettings(ctx context.Context, settings *db.Settings) error
}

func newAdminTestDB(t *testing.T, ctx context.Context) adminTestDB {
	t.Helper()

	client, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newAdminTestBotAPI(t *testing.T, handler func(method string, r *http.Request) any) *api.BotAPI {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()

		method := path.Base(r.URL.Path)
		var result any
		switch method {
		case "getMe":
			result = map[string]any{
				"id":                   1,
				adminTestJSONIsBot:     true,
				adminTestJSONFirstName: "Test",
				"username":             "testbot",
			}
		default:
			result = handler(method, r)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": result,
		}); err != nil {
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

func cloneForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	form := make(url.Values, len(r.Form))
	for key, values := range r.Form {
		form[key] = slices.Clone(values)
	}
	return form
}

func adminCommandMessage(chatID int64, chatType string, messageID int, text string) *api.Message {
	command := strings.Fields(text)[0]
	return &api.Message{
		MessageID: messageID,
		Text:      text,
		Chat:      api.Chat{ID: chatID, Type: chatType},
		From:      &api.User{ID: 7, IsBot: false, FirstName: adminTestUserName},
		Entities: []api.MessageEntity{
			{Type: "bot_command", Offset: 0, Length: len(command)},
		},
	}
}

func sentMessageResult(form url.Values) map[string]any {
	return map[string]any{
		"message_id": 100,
		"chat":       map[string]any{"id": mustFormInt64(form.Get("chat_id")), adminTestJSONType: panelChatTypePrivate},
		"text":       form.Get("text"),
	}
}

func firstAdminCall(t *testing.T, calls []adminTelegramCall, method string) adminTelegramCall {
	t.Helper()
	for _, call := range calls {
		if call.method == method {
			return call
		}
	}
	t.Fatalf("missing telegram method %s in calls %#v", method, calls)
	return adminTelegramCall{}
}

func countAdminCalls(calls []adminTelegramCall, method string) int {
	count := 0
	for _, call := range calls {
		if call.method == method {
			count++
		}
	}
	return count
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if condition() {
		return
	}
	t.Fatalf("condition was not met within %s", timeout)
}

func mustFormInt64(value string) int64 {
	var result int64
	if _, err := fmt.Sscan(value, &result); err != nil {
		return 0
	}
	return result
}

func mustFormInt(value string) int {
	var result int
	if _, err := fmt.Sscan(value, &result); err != nil {
		return 0
	}
	return result
}
