package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestAnnounceGroupAdminCommandsRegistersVoteBanForAllGroups(t *testing.T) {
	t.Parallel()

	type commandScope struct {
		Type string `json:"type"`
	}
	type command struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	type setCommandsCall struct {
		Scope    commandScope
		Commands []command
	}

	var calls []setCommandsCall
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()

		method := path.Base(r.URL.Path)
		result := any(true)
		switch method {
		case "getMe":
			result = map[string]any{
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
				"username":   "testbot",
			}
		case "deleteMyCommands":
			deleteCalls++
		case "setMyCommands":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			var scope commandScope
			if err := json.Unmarshal([]byte(r.Form.Get("scope")), &scope); err != nil {
				t.Fatalf("unmarshal scope: %v", err)
			}
			var commands []command
			if err := json.Unmarshal([]byte(r.Form.Get("commands")), &commands); err != nil {
				t.Fatalf("unmarshal commands: %v", err)
			}
			calls = append(calls, setCommandsCall{Scope: scope, Commands: commands})
		default:
			t.Fatalf("unexpected method %s", method)
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

	botAPI, err := api.NewBotAPIWithClient("TEST_TOKEN", fmt.Sprintf("%s/bot%%s/%%s", server.URL), server.Client())
	if err != nil {
		t.Fatalf("new bot api: %v", err)
	}
	if err := announceGroupAdminCommands(botAPI); err != nil {
		t.Fatalf("announce commands: %v", err)
	}

	if deleteCalls != 1 {
		t.Fatalf("deleteMyCommands calls = %d, want 1", deleteCalls)
	}

	var groupCommands []command
	var adminCommands []command
	for _, call := range calls {
		switch call.Scope.Type {
		case "all_group_chats":
			groupCommands = call.Commands
		case "all_chat_administrators":
			adminCommands = call.Commands
		}
	}
	if len(groupCommands) != 1 || groupCommands[0].Command != "voteban" {
		t.Fatalf("expected all-group voteban command, got %#v", groupCommands)
	}
	if len(adminCommands) != 1 || adminCommands[0].Command != "settings" {
		t.Fatalf("expected admin settings command, got %#v", adminCommands)
	}
}
