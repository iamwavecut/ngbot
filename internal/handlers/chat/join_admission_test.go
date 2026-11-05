package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

const testAdmissionAdministrator = "administrator"

type allowlistedAdmissionStore struct{ gatekeeperStore }

func (*allowlistedAdmissionStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return true, nil
}

func TestJoinRequestDefaultsToDMWithConfiguredWebApp(t *testing.T) {
	for _, scenario := range []struct {
		queryID     string
		allowlisted bool
	}{{}, {queryID: testJoinQueryID}, {queryID: testJoinQueryID, allowlisted: true}} {
		queryID := scenario.queryID
		t.Run(fmt.Sprintf("query_present_%t_allowlisted_%t", queryID != "", scenario.allowlisted), func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "admission.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			recorder := &botRequestRecorder{}
			botAPI := newTestBotAPI(t, func(method string, request *http.Request) any {
				recorder.record(t, method, request)
				switch method {
				case testTelegramMethodGetChat:
					if request.Form.Get("chat_id") == "9001" {
						return &testBotAPIError{code: 400, description: "Bad Request: chat not found"}
					}
					return map[string]any{"id": -100, testJSONType: testChatTypeSupergroup, testJSONTitle: testGroupTitle}
				case testTelegramMethodSendMessage:
					return map[string]any{logFieldMessageID: 81}
				case testTelegramMethodGetChatMember:
					return testChatMemberResponse(testMemberStatusLeft, false, false, false)
				default:
					return true
				}
			})
			settings := webAppSettings()
			g := &Gatekeeper{bot: botAPI, store: client, s: &gatekeeperTestService{settings: settings}, config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: testWebAppURL}}, banChecker: &testGatekeeperBanChecker{}}
			if scenario.allowlisted {
				g.store = &allowlistedAdmissionStore{gatekeeperStore: client}
			}
			request := &api.ChatJoinRequest{Chat: api.Chat{ID: -100, Type: testChatTypeSupergroup}, From: api.User{ID: 42, FirstName: testFirstNameNeo}, UserChatID: 9001, QueryID: queryID}
			if err := g.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings); err != nil {
				t.Fatal(err)
			}
			challenge, err := client.GetChallengeByChatUser(t.Context(), -100, 42)
			if err != nil || challenge == nil || challenge.ChallengeMessageID != 81 || challenge.WebAppToken != "" || challenge.JoinRequestQueryID != "" {
				t.Fatalf("join request did not create a native DM challenge: err=%v", err)
			}
			if len(recorder.byMethod(testTelegramMethodSendJoinWebApp)) != 0 {
				t.Fatal("default admission opened a WebApp")
			}
			sends := recorder.byMethod(testTelegramMethodSendMessage)
			if len(sends) != 1 || sends[0].form.Get("chat_id") != "9001" {
				t.Fatalf("DM did not use user_chat_id: %+v", sends)
			}
			callback := &api.CallbackQuery{ID: "answer", From: &request.From, Data: fmt.Sprintf("42;%s", challenge.SuccessUUID), Message: &api.Message{MessageID: 81, Chat: api.Chat{ID: 9001, Type: telegramChatTypePrivate}}}
			if err := g.handleChallenge(t.Context(), &api.Update{CallbackQuery: callback}, &callback.Message.Chat, &request.From); err != nil {
				t.Fatal(err)
			}
			approvals := recorder.byMethod(testTelegramMethodApproveJoinRequest)
			if len(approvals) != 1 || approvals[0].form.Get("chat_id") != "-100" || approvals[0].form.Get("user_id") != "42" {
				t.Fatalf("DM answer did not approve the target request: %+v", approvals)
			}
		})
	}
}

func TestUndeliverableDMCaptchaLeavesJoinRequestForReview(t *testing.T) {
	for _, queryID := range []string{"", testJoinQueryID} {
		t.Run(fmt.Sprintf("query_present_%t", queryID != ""), func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "admission.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			recorder := &botRequestRecorder{}
			botAPI := newTestBotAPI(t, func(method string, request *http.Request) any {
				recorder.record(t, method, request)
				switch method {
				case testTelegramMethodGetChat:
					return map[string]any{"id": -100, testJSONType: testChatTypeSupergroup, testJSONTitle: testGroupTitle}
				case testTelegramMethodGetChatMember:
					return testChatMemberResponse(testMemberStatusLeft, false, false, false)
				case testTelegramMethodSendMessage:
					return &testBotAPIError{code: 403, description: "Forbidden: bot can't initiate conversation with a user"}
				default:
					return true
				}
			})
			settings := webAppSettings()
			g := &Gatekeeper{bot: botAPI, store: client, s: &gatekeeperTestService{settings: settings}, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{}}
			request := &api.ChatJoinRequest{Chat: api.Chat{ID: -100}, From: api.User{ID: 42}, UserChatID: 9001, QueryID: queryID}
			if err := g.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{testTelegramMethodBanChatMember, testTelegramMethodDeclineJoinRequest, testTelegramMethodApproveJoinRequest} {
				if len(recorder.byMethod(method)) != 0 {
					t.Fatalf("unavailable DM caused %s", method)
				}
			}
			challenge, err := client.GetChallengeByChatUser(t.Context(), -100, 42)
			if err != nil || challenge != nil {
				t.Fatalf("undeliverable challenge still active: %+v %v", challenge, err)
			}
		})
	}
}

func TestAdminAdditionGrantsChatTrustThroughMandatoryRouter(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         string
		self           bool
		bot            bool
		serviceMessage bool
		viaRequest     bool
		wantTrust      bool
	}{
		{name: testAdmissionAdministrator, status: testAdmissionAdministrator, wantTrust: true},
		{name: "owner", status: "creator", wantTrust: true},
		{name: "service_message", status: testAdmissionAdministrator, serviceMessage: true, wantTrust: true},
		{name: "manual_approval", status: testAdmissionAdministrator, viaRequest: true, wantTrust: true},
		{name: "ordinary_member", status: telegramMemberStatus},
		{name: "former_admin", status: testMemberStatusLeft},
		{name: "self_join", status: testAdmissionAdministrator, self: true},
		{name: "bot_approval", status: testAdmissionAdministrator, bot: true, viaRequest: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "trust.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			settings := db.DefaultSettings(-100)
			settings.GatekeeperEnabled = false
			if err := client.SetSettings(t.Context(), settings); err != nil {
				t.Fatal(err)
			}
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				if method == testTelegramMethodGetChatMember {
					return testChatMemberResponse(test.status, false, false, false)
				}
				return true
			})
			now := time.Now().UTC().Truncate(time.Second)
			r := &Reactor{bot: botAPI, store: client, s: &testBotService{botAPI: botAPI, settings: settings}, now: func() time.Time { return now }}
			guard := NewBanlistGuard(botAPI, client, &testBanService{})
			router := NewModerationRouter(guard, r)
			chat := api.Chat{ID: -100, Type: testChatTypeSupergroup}
			user := api.User{ID: 42, FirstName: testFirstNameNeo}
			actor := api.User{ID: 17, IsBot: test.bot}
			if test.self {
				actor = user
			}
			update := &api.Update{UpdateID: 123, ChatMember: &api.ChatMemberUpdated{Chat: chat, From: actor, Date: now.Unix(), OldChatMember: api.ChatMember{User: &user, Status: testMemberStatusLeft}, NewChatMember: api.ChatMember{User: &user, Status: telegramMemberStatus}, ViaJoinRequest: test.viaRequest}}
			if test.serviceMessage {
				update.ChatMember = nil
				update.Message = &api.Message{Chat: chat, From: &actor, Date: now.Unix(), NewChatMembers: []api.User{user}}
			}
			if _, err := router.Handle(t.Context(), update, &chat, &actor); err != nil {
				t.Fatal(err)
			}
			trust, err := client.MessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID})
			if err != nil {
				t.Fatal(err)
			}
			if got := trust != nil && trust.Trusted(now); got != test.wantTrust {
				t.Fatalf("trusted=%t want=%t trust=%+v", got, test.wantTrust, trust)
			}
			if !test.wantTrust {
				return
			}
			if !trust.TrustedUntil.Time.Equal(now.Add(30*24*time.Hour)) || trust.SafeMessages != 0 {
				t.Fatalf("admin trust changed safe-message evidence: %+v", trust)
			}
			settings.GatekeeperEnabled, settings.GatekeeperCaptchaEnabled = true, true
			g := &Gatekeeper{bot: botAPI, store: client, s: &gatekeeperTestService{settings: settings}, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{}}
			joined := &api.Update{ChatMember: &api.ChatMemberUpdated{Chat: chat, From: actor, Date: now.Unix(), OldChatMember: api.ChatMember{User: &user, Status: testMemberStatusLeft}, NewChatMember: api.ChatMember{User: &user, Status: telegramMemberStatus}}}
			if err := g.handleChatMember(t.Context(), joined, settings); err != nil {
				t.Fatal(err)
			}
			if challenge, err := client.GetChallengeByChatUser(t.Context(), chat.ID, user.ID); err != nil || challenge != nil {
				t.Fatalf("administrator admission created CAPTCHA: err=%v", err)
			}
			if err := client.ResetMessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := router.Handle(t.Context(), update, &chat, &actor); err != nil {
				t.Fatal(err)
			}
			trust, err = client.MessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID})
			if err != nil || (trust != nil && trust.Trusted(now)) {
				t.Fatalf("replayed addition restored revoked trust: %+v %v", trust, err)
			}
		})
	}
}

func TestExpiredDMCaptchaNeverBansOnCallback(t *testing.T) {
	for _, queryID := range []string{"", "legacy-query"} {
		t.Run(fmt.Sprintf("query_present_%t", queryID != ""), func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "expired.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			recorder := &botRequestRecorder{}
			botAPI := newTestBotAPI(t, func(method string, request *http.Request) any {
				recorder.record(t, method, request)
				if method == testTelegramMethodGetChat {
					return map[string]any{"id": -100, testJSONType: testChatTypeSupergroup, testJSONTitle: testGroupTitle}
				}
				return true
			})
			settings := webAppSettings()
			g := &Gatekeeper{bot: botAPI, store: client, s: &gatekeeperTestService{settings: settings}, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{}}
			challenge := &db.Challenge{CommChatID: 9001, ChatID: -100, UserID: 42, Status: db.ChallengeStatusPending, SuccessUUID: "correct", JoinRequestQueryID: queryID, ChallengeMessageID: 81, CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute)}
			if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
				t.Fatal(err)
			}
			user := &api.User{ID: 42}
			chat := &api.Chat{ID: 9001, Type: telegramChatTypePrivate}
			update := &api.Update{CallbackQuery: &api.CallbackQuery{ID: "answer", From: user, Data: "42;correct", Message: &api.Message{MessageID: 81, Chat: *chat}}}
			if err := g.handleChallenge(t.Context(), update, chat, user); err != nil {
				t.Fatal(err)
			}
			if len(recorder.byMethod(testTelegramMethodBanChatMember)) != 0 || len(recorder.byMethod(testTelegramMethodDeclineJoinRequest)) != 0 {
				t.Fatal("expired DM punished applicant")
			}
			if stored, err := client.GetChallengeByChatUser(t.Context(), -100, 42); err != nil || stored != nil {
				t.Fatalf("expired challenge remained active: err=%v", err)
			}
		})
	}
}

func TestBanlistJoinRequestDeclinesWithoutBanAndSurvivesReplay(t *testing.T) {
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "admission.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, request *http.Request) any { recorder.record(t, method, request); return true })
	service := &testBanService{knownBanned: true}
	guard := NewBanlistGuard(botAPI, client, service)
	chat := api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := api.User{ID: 42}
	update := &api.Update{UpdateID: 123, ChatJoinRequest: &api.ChatJoinRequest{Chat: chat, From: user, UserChatID: 9001}}
	for range 2 {
		if proceed, err := guard.Handle(t.Context(), update, &chat, &user); err != nil || proceed {
			t.Fatalf("terminal join handling: proceed=%t err=%v", proceed, err)
		}
	}
	declines := recorder.byMethod(testTelegramMethodDeclineJoinRequest)
	if len(declines) != 1 || declines[0].form.Get("chat_id") != "-100" || declines[0].form.Get("user_id") != "42" || len(service.bans) != 0 {
		t.Fatalf("join request was not declined exactly once without a ban: declines=%+v bans=%+v", declines, service.bans)
	}
}
