package handlers

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

func TestMain(m *testing.M) {
	i18n.Init()
	os.Exit(m.Run())
}

func TestDMLanguageResolution(t *testing.T) {
	t.Parallel()

	g := &Gatekeeper{config: &config.Config{DefaultLanguage: "en"}}
	cases := []struct {
		name   string
		stored string
		user   *api.User
		want   string
	}{
		{"live user language wins over stored", "ru", &api.User{LanguageCode: "de"}, "de"},
		{"stored used when user is nil", "ru", nil, "ru"},
		{"stored used when user language empty", "ru", &api.User{LanguageCode: ""}, "ru"},
		{"stored used when user language unsupported", "ru", &api.User{LanguageCode: "xx"}, "ru"},
		{"default when user nil and stored unsupported", "xx", nil, "en"},
		{"default when both empty", "", &api.User{LanguageCode: ""}, "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.dmLanguage(tc.stored, tc.user); got != tc.want {
				t.Fatalf("dmLanguage(%q, %+v) = %q, want %q", tc.stored, tc.user, got, tc.want)
			}
		})
	}
}

func TestStartJoinRequestWebAppChallengeStoresUserLanguage(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodSendJoinWebApp:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: testWebAppURL}},
		banChecker: &testGatekeeperBanChecker{},
	}

	req := &api.ChatJoinRequest{
		Chat:       api.Chat{ID: -100123, Type: testChatTypeSupergroup},
		From:       api.User{ID: 42, FirstName: testFirstNameNeo, LanguageCode: "ru"},
		UserChatID: 9001,
		QueryID:    testJoinQueryID,
	}
	if err := gatekeeper.startJoinRequestWebAppChallenge(context.Background(), req, webAppSettings()); err != nil {
		t.Fatalf("startJoinRequestWebAppChallenge returned error: %v", err)
	}

	challenge := store.onlyChallenge(t)
	if challenge.UserLanguage != "ru" {
		t.Fatalf("expected stored user_language ru, got %q", challenge.UserLanguage)
	}
}

func TestUnopenedWebAppFallbackCarriesUserLanguage(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)

		switch method {
		case testTelegramMethodGetChat:
			switch r.Form.Get("chat_id") {
			case "9001":
				return map[string]any{"id": 9001, "type": telegramChatTypePrivate, testJSONFirstName: testFirstNameNeo}
			case "-100123":
				return map[string]any{"id": -100123, "type": testChatTypeSupergroup, testJSONTitle: testGroupTitle}
			default:
				t.Fatalf("unexpected getChat chat_id: %q", r.Form.Get("chat_id"))
				return nil
			}
		case testTelegramMethodSendMessage:
			return recorder.nextSendMessageResult()
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	unopened := &db.Challenge{
		CommChatID:         9001,
		UserID:             42,
		ChatID:             -100123,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        testCorrectChoice,
		WebAppToken:        testToken,
		JoinRequestQueryID: testJoinQueryID,
		CaptchaPrompt:      "poodle",
		CaptchaOptionsJSON: testCaptchaOptionsJSON,
		UserLanguage:       "ru",
		CreatedAt:          time.Now().Add(-30 * time.Second),
		ExpiresAt:          time.Now().Add(2 * time.Minute),
	}
	if _, err := store.CreateChallenge(context.Background(), unopened); err != nil {
		t.Fatalf("create unopened challenge: %v", err)
	}

	gatekeeper := &Gatekeeper{
		bot: botAPI,
		s: &gatekeeperTestService{
			testBotService: testBotService{botAPI: botAPI, language: "en"},
			settings: &db.Settings{
				GatekeeperEnabled:        true,
				GatekeeperCaptchaEnabled: true,
			},
		},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	if err := gatekeeper.processUnopenedWebAppChallenges(context.Background()); err != nil {
		t.Fatalf("processUnopenedWebAppChallenges returned error: %v", err)
	}

	challenge := store.onlyChallenge(t)
	if challenge.UserLanguage != "ru" {
		t.Fatalf("expected user_language carried to DM fallback challenge, got %q", challenge.UserLanguage)
	}
	if challenge.WebAppToken != "" {
		t.Fatalf("expected web app token cleared after fallback, got %q", challenge.WebAppToken)
	}
}
