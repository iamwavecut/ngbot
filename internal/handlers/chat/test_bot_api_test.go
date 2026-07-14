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

const (
	testFirstNameUser      = "User"
	testFirstNameNeo       = "Neo"
	testFirstNameAdmin     = "Admin"
	testFirstNameActor     = "Actor"
	testFirstNameTarget    = "Target"
	testFirstNameBad       = "Bad"
	testGroupTitle         = "Wave Club"
	testSpamChannelTitle   = "Spam Channel"
	testGreetingTemplate   = "GREETING {user} to {chat_title}"
	testWebAppURL          = "https://guard.example"
	testCorrectChoice      = "correct-choice"
	testCaptchaOptionsJSON = `[{"id":"correct-choice","symbol":"A"},{"id":"wrong-choice","symbol":"B"}]`
	testJoinRequestDecline = "decline"
	testWebAppFormChoice   = "choice"
	testWebAppFormInitData = "init_data"
	testEntityBotCommand   = "bot_command"
	testVoteBanCommand     = "/voteban"
	testJSONIsBot          = "is_bot"
	testJSONFirstName      = "first_name"
	testJSONDate           = "date"
	testJSONDescription    = "description"
	testJSONTitle          = "title"
	testJSONType           = "type"
	testChatTypeChannel    = "channel"
	testChatTypeSupergroup = "supergroup"
	testMemberStatusLeft   = "left"
	testGroupUsername      = "waveclub"
	testJoinQueryID        = "join-query"
	testExpiredChallengeID = "uuid-expired"
	testToken              = "tok"
	testBotUsername        = "testbot"
	testCommandBotUsername = "ngbot"
	testBadUsername        = "badworker"
	testChallengePrompt    = "poodle"
	testChallengePromptRU  = "пуделя"
	testWrongChoice        = "wrong-choice"
	testWebAppFormToken    = "token"
	testMessageText        = "hello there"
)

type testBotAPIError struct {
	code        int
	description string
}

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
					"id":              1,
					testJSONIsBot:     true,
					testJSONFirstName: "Test",
					logFieldUsername:  testBotUsername,
				},
			}); err != nil {
				t.Fatalf("encode getMe response: %v", err)
			}
			return
		}

		result := handler(method, r)

		w.Header().Set("Content-Type", "application/json")
		if forcedErr, ok := result.(*testBotAPIError); ok {
			if err := json.NewEncoder(w).Encode(map[string]any{
				"ok":                false,
				"error_code":        forcedErr.code,
				testJSONDescription: forcedErr.description,
			}); err != nil {
				t.Fatalf("encode forced error response: %v", err)
			}
			return
		}
		if code, forced := failures[method]; forced && code != 0 {
			if err := json.NewEncoder(w).Encode(map[string]any{
				"ok":                false,
				"error_code":        code,
				testJSONDescription: method + " forced failure",
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
