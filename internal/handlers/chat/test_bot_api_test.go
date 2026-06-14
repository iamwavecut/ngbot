package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func newTestBotAPI(t *testing.T, handler func(method string, r *http.Request) any) *api.BotAPI {
	t.Helper()
	return newTestBotAPIWithErrors(t, handler, nil)
}

func newTestBotAPIWithErrors(t *testing.T, handler func(method string, r *http.Request) any, failures map[string]int) *api.BotAPI {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()

		method := path.Base(r.URL.Path)

		if method == "getMe" {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"id":         1,
					"is_bot":     true,
					"first_name": "Test",
					"username":   "testbot",
				},
			}); err != nil {
				t.Fatalf("encode getMe response: %v", err)
			}
			return
		}

		result := handler(method, r)

		w.Header().Set("Content-Type", "application/json")
		if code, forced := failures[method]; forced && code != 0 {
			if err := json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  code,
				"description": method + " forced failure",
			}); err != nil {
				t.Fatalf("encode error response: %v", err)
			}
			return
		}
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
		t.Fatalf("new test bot api: %v", err)
	}
	return botAPI
}
