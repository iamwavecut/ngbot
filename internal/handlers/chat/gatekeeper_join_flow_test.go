package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

type recordedBotRequest struct {
	method string
	form   url.Values
}

type botRequestRecorder struct {
	requests      []recordedBotRequest
	nextMessageID int
}

func (r *botRequestRecorder) record(t *testing.T, method string, req *http.Request) {
	t.Helper()

	if err := req.ParseForm(); err != nil {
		t.Fatalf("parse form for %s: %v", method, err)
	}

	form := make(url.Values, len(req.Form))
	for key, values := range req.Form {
		form[key] = append([]string(nil), values...)
	}
	r.requests = append(r.requests, recordedBotRequest{method: method, form: form})
}

func (r *botRequestRecorder) byMethod(method string) []recordedBotRequest {
	matches := make([]recordedBotRequest, 0)
	for _, req := range r.requests {
		if req.method == method {
			matches = append(matches, req)
		}
	}
	return matches
}

func (r *botRequestRecorder) nextSendMessageResult() any {
	r.nextMessageID++
	return map[string]any{
		"message_id": r.nextMessageID,
	}
}

type gatekeeperTestService struct {
	testBotService
	settings *db.Settings
}

func (s *gatekeeperTestService) GetSettings(context.Context, int64) (*db.Settings, error) {
	return s.settings, nil
}

type gatekeeperFlowStore struct {
	challenges map[string]*db.Challenge
}

func newGatekeeperFlowStore() *gatekeeperFlowStore {
	return &gatekeeperFlowStore{
		challenges: make(map[string]*db.Challenge),
	}
}

func (s *gatekeeperFlowStore) CreateChallenge(_ context.Context, challenge *db.Challenge) (*db.Challenge, error) {
	s.challenges[s.challengeKey(challenge.CommChatID, challenge.UserID, challenge.ChatID)] = cloneChallenge(challenge)
	return challenge, nil
}

func (s *gatekeeperFlowStore) GetChallengeByMessage(_ context.Context, commChatID, userID int64, challengeMessageID int) (*db.Challenge, error) {
	for _, challenge := range s.challenges {
		if challenge.CommChatID == commChatID && challenge.UserID == userID && challenge.ChallengeMessageID == challengeMessageID {
			return cloneChallenge(challenge), nil
		}
	}
	return nil, nil
}

func (s *gatekeeperFlowStore) GetChallengeByChatUser(_ context.Context, chatID, userID int64) (*db.Challenge, error) {
	var latest *db.Challenge
	for _, challenge := range s.challenges {
		if challenge.ChatID != chatID || challenge.UserID != userID {
			continue
		}
		if latest == nil || challenge.CreatedAt.After(latest.CreatedAt) {
			latest = challenge
		}
	}
	return cloneChallenge(latest), nil
}

func (s *gatekeeperFlowStore) UpdateChallenge(_ context.Context, challenge *db.Challenge) error {
	s.challenges[s.challengeKey(challenge.CommChatID, challenge.UserID, challenge.ChatID)] = cloneChallenge(challenge)
	return nil
}

func (s *gatekeeperFlowStore) DeleteChallenge(_ context.Context, commChatID, userID, chatID int64) error {
	delete(s.challenges, s.challengeKey(commChatID, userID, chatID))
	return nil
}

func (s *gatekeeperFlowStore) GetExpiredChallenges(_ context.Context, now time.Time) ([]*db.Challenge, error) {
	expired := make([]*db.Challenge, 0)
	for _, challenge := range s.challenges {
		if !challenge.ExpiresAt.After(now) {
			expired = append(expired, cloneChallenge(challenge))
		}
	}
	return expired, nil
}

func (s *gatekeeperFlowStore) AddChatRecentJoiner(_ context.Context, joiner *db.RecentJoiner) (*db.RecentJoiner, error) {
	return joiner, nil
}

func (s *gatekeeperFlowStore) GetUnprocessedRecentJoiners(context.Context) ([]*db.RecentJoiner, error) {
	return nil, nil
}

func (s *gatekeeperFlowStore) ProcessRecentJoiner(context.Context, int64, int64, bool) error {
	return nil
}

func (s *gatekeeperFlowStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return false, nil
}

func (s *gatekeeperFlowStore) onlyChallenge(t *testing.T) *db.Challenge {
	t.Helper()

	if len(s.challenges) != 1 {
		t.Fatalf("expected one challenge, got %d", len(s.challenges))
	}
	for _, challenge := range s.challenges {
		return cloneChallenge(challenge)
	}
	return nil
}

func (s *gatekeeperFlowStore) challengeKey(commChatID, userID, chatID int64) string {
	return fmt.Sprintf("%d:%d:%d", commChatID, userID, chatID)
}

func cloneChallenge(challenge *db.Challenge) *db.Challenge {
	if challenge == nil {
		return nil
	}
	clone := *challenge
	return &clone
}

func TestJoinRequestCaptchaSuccessHandoffSkipsSecondCaptchaAndSendsGreetingOnce(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	groupChat := api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club", UserName: "waveclub"}
	user := api.User{ID: 42, FirstName: "Neo"}
	store := newGatekeeperFlowStore()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)

		switch method {
		case "getChat":
			return map[string]any{
				"id":         9001,
				"type":       "private",
				"first_name": "Neo",
			}
		case "approveChatJoinRequest":
			handoffChallenge := store.onlyChallenge(t)
			if handoffChallenge.Status != db.ChallengeStatusPassedWaitingMemberJoin {
				t.Fatalf("expected handoff status before approve, got %q", handoffChallenge.Status)
			}
			return true
		case "sendMessage":
			return recorder.nextSendMessageResult()
		case "deleteMessage":
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	settings := &db.Settings{
		GatekeeperEnabled:             true,
		GatekeeperCaptchaEnabled:      true,
		GatekeeperGreetingEnabled:     true,
		GatekeeperGreetingText:        "GREETING {user} to {chat_title}",
		GatekeeperCaptchaOptionsCount: 3,
		ChallengeTimeout:              (3 * time.Minute).Nanoseconds(),
	}
	gatekeeper := &Gatekeeper{
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: settings},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	update := &api.Update{
		ChatJoinRequest: &api.ChatJoinRequest{
			Chat:       groupChat,
			From:       user,
			UserChatID: 9001,
		},
	}
	if err := gatekeeper.handleChatJoinRequest(context.Background(), update, settings); err != nil {
		t.Fatalf("handleChatJoinRequest returned error: %v", err)
	}

	requestMessages := recorder.byMethod("sendMessage")
	if len(requestMessages) != 1 {
		t.Fatalf("expected one DM challenge message, got %d", len(requestMessages))
	}
	if got := requestMessages[0].form.Get("chat_id"); got != "9001" {
		t.Fatalf("unexpected DM challenge chat id: %s", got)
	}
	if requestMessages[0].form.Get("reply_markup") == "" {
		t.Fatal("expected DM challenge to include captcha buttons")
	}
	if strings.Contains(requestMessages[0].form.Get("text"), "GREETING") {
		t.Fatalf("expected DM challenge to exclude greeting text, got %q", requestMessages[0].form.Get("text"))
	}

	challenge := store.onlyChallenge(t)
	if challenge.Status != db.ChallengeStatusPending {
		t.Fatalf("unexpected pending challenge status: %q", challenge.Status)
	}

	target := &api.ChatFullInfo{Chat: groupChat}
	if err := gatekeeper.completeChallenge(context.Background(), challenge, target, "en"); err != nil {
		t.Fatalf("completeChallenge returned error: %v", err)
	}

	handoffChallenge := store.onlyChallenge(t)
	if handoffChallenge.Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("unexpected handoff challenge status: %q", handoffChallenge.Status)
	}

	requestMessages = recorder.byMethod("sendMessage")
	if len(requestMessages) != 2 {
		t.Fatalf("expected DM challenge plus DM success message, got %d sends", len(requestMessages))
	}
	if got := requestMessages[1].form.Get("chat_id"); got != "9001" {
		t.Fatalf("unexpected DM success chat id: %s", got)
	}
	if requestMessages[1].form.Get("reply_markup") != "" {
		t.Fatal("expected DM success message without captcha buttons")
	}

	newMemberUpdate := &api.Update{
		Message: &api.Message{
			MessageID:      77,
			Chat:           groupChat,
			NewChatMembers: []api.User{user},
		},
	}
	if err := gatekeeper.handleNewChatMembersV2(context.Background(), newMemberUpdate, &groupChat, settings); err != nil {
		t.Fatalf("handleNewChatMembersV2 returned error: %v", err)
	}

	requestMessages = recorder.byMethod("sendMessage")
	if len(requestMessages) != 3 {
		t.Fatalf("expected DM challenge, DM success, and one group greeting, got %d sends", len(requestMessages))
	}
	if got := requestMessages[2].form.Get("chat_id"); got != "-100123" {
		t.Fatalf("unexpected group greeting chat id: %s", got)
	}
	if requestMessages[2].form.Get("reply_markup") != "" {
		t.Fatal("expected group greeting without captcha buttons")
	}
	if !strings.Contains(requestMessages[2].form.Get("text"), "GREETING") {
		t.Fatalf("expected group greeting text, got %q", requestMessages[2].form.Get("text"))
	}

	if len(store.challenges) != 0 {
		t.Fatalf("expected handoff challenge to be deleted after member join, got %d rows", len(store.challenges))
	}
	if len(recorder.byMethod("approveChatJoinRequest")) != 1 {
		t.Fatalf("expected one join request approval, got %d", len(recorder.byMethod("approveChatJoinRequest")))
	}
	if len(recorder.byMethod("deleteMessage")) != 1 {
		t.Fatalf("expected one DM challenge cleanup, got %d", len(recorder.byMethod("deleteMessage")))
	}
}

func TestDirectJoinCaptchaIncludesGreetingImmediatelyAndDoesNotRepeatAfterSuccess(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	groupChat := api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club", UserName: "waveclub"}
	user := api.User{ID: 42, FirstName: "Neo"}

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)

		switch method {
		case "sendMessage":
			return recorder.nextSendMessageResult()
		case "restrictChatMember", "deleteMessage":
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	settings := &db.Settings{
		GatekeeperEnabled:             true,
		GatekeeperCaptchaEnabled:      true,
		GatekeeperGreetingEnabled:     true,
		GatekeeperGreetingText:        "GREETING {user} to {chat_title}",
		GatekeeperCaptchaOptionsCount: 3,
		ChallengeTimeout:              (3 * time.Minute).Nanoseconds(),
	}
	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: settings},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	update := &api.Update{
		Message: &api.Message{
			MessageID:      55,
			Chat:           groupChat,
			NewChatMembers: []api.User{user},
		},
	}
	if err := gatekeeper.handleNewChatMembersV2(context.Background(), update, &groupChat, settings); err != nil {
		t.Fatalf("handleNewChatMembersV2 returned error: %v", err)
	}

	sendMessages := recorder.byMethod("sendMessage")
	if len(sendMessages) != 1 {
		t.Fatalf("expected one group captcha message, got %d", len(sendMessages))
	}
	if got := sendMessages[0].form.Get("chat_id"); got != "-100123" {
		t.Fatalf("unexpected group captcha chat id: %s", got)
	}
	if sendMessages[0].form.Get("reply_markup") == "" {
		t.Fatal("expected group captcha to include buttons")
	}
	if !strings.Contains(sendMessages[0].form.Get("text"), "GREETING") {
		t.Fatalf("expected pre-success captcha message to include greeting text, got %q", sendMessages[0].form.Get("text"))
	}

	challenge := store.onlyChallenge(t)
	if err := gatekeeper.completeChallenge(context.Background(), challenge, &api.ChatFullInfo{Chat: groupChat}, "en"); err != nil {
		t.Fatalf("completeChallenge returned error: %v", err)
	}

	sendMessages = recorder.byMethod("sendMessage")
	if len(sendMessages) != 1 {
		t.Fatalf("expected no extra greeting after success, got %d sends", len(sendMessages))
	}

	if len(store.challenges) != 0 {
		t.Fatalf("expected direct-join challenge to be deleted after success, got %d rows", len(store.challenges))
	}
}

func TestJoinRequestGreetingWithoutCaptchaLeavesManualReviewUntouched(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})

	settings := &db.Settings{
		GatekeeperEnabled:         true,
		GatekeeperCaptchaEnabled:  false,
		GatekeeperGreetingEnabled: true,
		GatekeeperGreetingText:    "GREETING {user}",
	}
	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: settings},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	update := &api.Update{
		ChatJoinRequest: &api.ChatJoinRequest{
			Chat:       api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club"},
			From:       api.User{ID: 42, FirstName: "Neo"},
			UserChatID: 9001,
		},
	}
	if err := gatekeeper.handleChatJoinRequest(context.Background(), update, settings); err != nil {
		t.Fatalf("handleChatJoinRequest returned error: %v", err)
	}

	if len(recorder.requests) != 0 {
		t.Fatalf("expected no bot requests, got %d", len(recorder.requests))
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected no challenge rows, got %d", len(store.challenges))
	}
}

func TestProcessExpiredJoinRequestChallengesCleanupWithoutApproval(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		settings *db.Settings
	}{
		{
			name: "expired pending join request",
			settings: &db.Settings{
				GatekeeperEnabled:        true,
				GatekeeperCaptchaEnabled: true,
			},
		},
		{
			name: "disabled pending join request",
			settings: &db.Settings{
				GatekeeperEnabled:        false,
				GatekeeperCaptchaEnabled: true,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &botRequestRecorder{}
			botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
				recorder.record(t, method, r)

				switch method {
				case "deleteMessage":
					return true
				default:
					t.Fatalf("unexpected bot method: %s", method)
					return nil
				}
			})

			store := newGatekeeperFlowStore()
			expiredChallenge := &db.Challenge{
				CommChatID:         9001,
				UserID:             42,
				ChatID:             -100123,
				Status:             db.ChallengeStatusPending,
				SuccessUUID:        "uuid-expired",
				ChallengeMessageID: 501,
				CreatedAt:          time.Now().Add(-10 * time.Minute),
				ExpiresAt:          time.Now().Add(-time.Minute),
			}
			if _, err := store.CreateChallenge(context.Background(), expiredChallenge); err != nil {
				t.Fatalf("create expired challenge: %v", err)
			}

			gatekeeper := &Gatekeeper{
				s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: tc.settings},
				store:      store,
				config:     &config.Config{},
				banChecker: &testGatekeeperBanChecker{},
			}

			if err := gatekeeper.processExpiredChallenges(context.Background()); err != nil {
				t.Fatalf("processExpiredChallenges returned error: %v", err)
			}

			if len(recorder.byMethod("deleteMessage")) != 1 {
				t.Fatalf("expected one DM challenge cleanup, got %d", len(recorder.byMethod("deleteMessage")))
			}
			if len(recorder.byMethod("approveChatJoinRequest")) != 0 {
				t.Fatalf("expected no join request approvals, got %d", len(recorder.byMethod("approveChatJoinRequest")))
			}
			if len(recorder.byMethod("declineChatJoinRequest")) != 0 {
				t.Fatalf("expected no join request declines, got %d", len(recorder.byMethod("declineChatJoinRequest")))
			}
			if len(recorder.byMethod("banChatMember")) != 0 {
				t.Fatalf("expected no bans, got %d", len(recorder.byMethod("banChatMember")))
			}
			if len(store.challenges) != 0 {
				t.Fatalf("expected expired join-request challenge cleanup to remove the row, got %d", len(store.challenges))
			}
		})
	}
}
