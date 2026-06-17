package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestBanUserFromChatRevokesMessages(t *testing.T) {
	t.Parallel()

	var banCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()

		method := path.Base(r.URL.Path)
		var result any = true
		switch method {
		case "getMe":
			result = map[string]any{
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
				"username":   "testbot",
			}
		case "banChatMember":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			banCalled = true
			if got := r.Form.Get("chat_id"); got != "-100" {
				t.Fatalf("chat_id = %q, want -100", got)
			}
			if got := r.Form.Get("user_id"); got != "200" {
				t.Fatalf("user_id = %q, want 200", got)
			}
			if got := r.Form.Get("revoke_messages"); got != "true" {
				t.Fatalf("revoke_messages = %q, want true", got)
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
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
	if err := BanUserFromChat(context.Background(), botAPI, 200, -100, 0); err != nil {
		t.Fatalf("ban user: %v", err)
	}
	if !banCalled {
		t.Fatal("expected banChatMember call")
	}
}
