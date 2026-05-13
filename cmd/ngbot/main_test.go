package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestConfigureUpdatesRequestsMessageReactionsOnly(t *testing.T) {
	t.Parallel()

	updates := configureUpdates(time.Minute).AllowedUpdates
	if !slices.Contains(updates, "message_reaction") {
		t.Fatalf("expected message_reaction updates, got %#v", updates)
	}
	if slices.Contains(updates, "message_reaction_count") {
		t.Fatalf("did not expect message_reaction_count updates, got %#v", updates)
	}
}

type commandRegistrationCall struct {
	method   string
	scope    string
	commands []api.BotCommand
}

func TestAnnounceBotCommandsRegistersPrivateHelp(t *testing.T) {
	var calls []commandRegistrationCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		switch method {
		case "getMe":
			writeTelegramResult(t, w, map[string]any{
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
				"username":   "testbot",
			})
			return
		case "deleteMyCommands":
			calls = append(calls, commandRegistrationCall{method: method})
			writeTelegramResult(t, w, true)
			return
		case "setMyCommands":
			form := parseRegistrationForm(t, r)
			calls = append(calls, commandRegistrationCall{
				method:   method,
				scope:    commandScopeType(t, form),
				commands: commandList(t, form),
			})
			writeTelegramResult(t, w, true)
			return
		default:
			t.Fatalf("unexpected telegram method: %s", method)
		}
	}))
	t.Cleanup(server.Close)

	botAPI, err := api.NewBotAPIWithClient("TEST_TOKEN", fmt.Sprintf("%s/bot%%s/%%s", server.URL), server.Client())
	if err != nil {
		t.Fatalf("new bot api: %v", err)
	}

	if err := announceBotCommands(botAPI); err != nil {
		t.Fatalf("announce bot commands: %v", err)
	}

	if len(calls) != 4 {
		t.Fatalf("registration calls = %#v, want delete + 3 set calls", calls)
	}

	private := commandsForScope(t, calls, "all_private_chats")
	if !slices.Equal(private, []api.BotCommand{{Command: "help", Description: "Show bot help"}}) {
		t.Fatalf("private commands = %#v", private)
	}

	group := commandsForScope(t, calls, "all_group_chats")
	if !slices.Equal(group, []api.BotCommand{{Command: "voteban", Description: "Report spam with community voting"}}) {
		t.Fatalf("group commands = %#v", group)
	}

	admin := commandsForScope(t, calls, "all_chat_administrators")
	wantAdmin := []api.BotCommand{
		{Command: "voteban", Description: "Report spam with community voting"},
		{Command: "settings", Description: "Bot settings"},
	}
	if !slices.Equal(admin, wantAdmin) {
		t.Fatalf("admin commands = %#v", admin)
	}
}

func writeTelegramResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func parseRegistrationForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return r.Form
}

func commandScopeType(t *testing.T, form url.Values) string {
	t.Helper()
	var scope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(form.Get("scope")), &scope); err != nil {
		t.Fatalf("unmarshal scope %q: %v", form.Get("scope"), err)
	}
	return scope.Type
}

func commandList(t *testing.T, form url.Values) []api.BotCommand {
	t.Helper()
	var commands []api.BotCommand
	if err := json.Unmarshal([]byte(form.Get("commands")), &commands); err != nil {
		t.Fatalf("unmarshal commands %q: %v", form.Get("commands"), err)
	}
	return commands
}

func commandsForScope(t *testing.T, calls []commandRegistrationCall, scope string) []api.BotCommand {
	t.Helper()
	for _, call := range calls {
		if call.scope == scope {
			return call.commands
		}
	}
	t.Fatalf("missing commands for scope %s in %#v", scope, calls)
	return nil
}
