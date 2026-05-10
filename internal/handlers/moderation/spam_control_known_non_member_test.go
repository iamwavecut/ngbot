package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

type testModerationService struct {
	botAPI *api.BotAPI
}

func (s *testModerationService) GetBot() *api.BotAPI {
	return s.botAPI
}

func (s *testModerationService) GetDB() db.Client {
	return nil
}

func (s *testModerationService) IsMember(context.Context, int64, int64) (bool, error) {
	return false, nil
}

func (s *testModerationService) InsertMember(context.Context, int64, int64) error {
	return nil
}

func (s *testModerationService) DeleteMember(context.Context, int64, int64) error {
	return nil
}

func (s *testModerationService) GetSettings(context.Context, int64) (*db.Settings, error) {
	return nil, nil
}

func (s *testModerationService) SetSettings(context.Context, *db.Settings) error {
	return nil
}

func (s *testModerationService) GetLanguage(context.Context, int64, *api.User) string {
	return "en"
}

type testModerationStore struct {
	spamCase              *db.SpamCase
	votes                 []*db.SpamVote
	recentJoiners         []*db.RecentJoiner
	processedJoiners      [][3]int64
	deletedKnownNonMember [][2]int64
}

func (s *testModerationStore) CreateSpamCase(_ context.Context, sc *db.SpamCase) (*db.SpamCase, error) {
	sc.ID = 1
	s.spamCase = sc
	return sc, nil
}

func (s *testModerationStore) UpdateSpamCase(_ context.Context, sc *db.SpamCase) error {
	copyCase := *sc
	s.spamCase = &copyCase
	return nil
}

func (s *testModerationStore) GetSpamCase(context.Context, int64) (*db.SpamCase, error) {
	return s.spamCase, nil
}

func (s *testModerationStore) GetActiveSpamCase(context.Context, int64, int64) (*db.SpamCase, error) {
	if s.spamCase == nil || s.spamCase.Status != spamCaseStatusPending {
		return nil, nil
	}
	return s.spamCase, nil
}

func (s *testModerationStore) AddSpamVote(context.Context, *db.SpamVote) error {
	return nil
}

func (s *testModerationStore) GetSpamVotes(context.Context, int64) ([]*db.SpamVote, error) {
	return s.votes, nil
}

func (s *testModerationStore) GetMembers(context.Context, int64) ([]int64, error) {
	return nil, nil
}

func (s *testModerationStore) GetChatRecentJoiners(_ context.Context, chatID int64) ([]*db.RecentJoiner, error) {
	joiners := make([]*db.RecentJoiner, 0)
	for _, joiner := range s.recentJoiners {
		if joiner.ChatID == chatID {
			joiners = append(joiners, joiner)
		}
	}
	return joiners, nil
}

func (s *testModerationStore) ProcessRecentJoiner(_ context.Context, chatID int64, userID int64, isSpammer bool) error {
	spammer := int64(0)
	if isSpammer {
		spammer = 1
	}
	s.processedJoiners = append(s.processedJoiners, [3]int64{chatID, userID, spammer})
	return nil
}

func (s *testModerationStore) DeleteChatKnownNonMember(_ context.Context, chatID int64, userID int64) error {
	s.deletedKnownNonMember = append(s.deletedKnownNonMember, [2]int64{chatID, userID})
	return nil
}

type testModerationBanService struct{}

func (s *testModerationBanService) Start(context.Context) error { return nil }
func (s *testModerationBanService) Stop(context.Context) error  { return nil }
func (s *testModerationBanService) CheckBan(context.Context, int64) (bool, error) {
	return false, nil
}

func (s *testModerationBanService) MuteUser(context.Context, int64, int64) error {
	return nil
}

func (s *testModerationBanService) UnmuteUser(context.Context, int64, int64) error {
	return nil
}

func (s *testModerationBanService) BanUserWithMessage(context.Context, int64, int64, int) error {
	return nil
}

func (s *testModerationBanService) UnbanUser(context.Context, int64, int64) error {
	return nil
}

func (s *testModerationBanService) IsRestricted(context.Context, int64, int64) (bool, error) {
	return false, nil
}

func (s *testModerationBanService) IsKnownBanned(int64) bool {
	return false
}

func newModerationTestBotAPI(t *testing.T, handler func(method string, r *http.Request) any) *api.BotAPI {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()

		method := path.Base(r.URL.Path)
		var result any
		switch method {
		case "getMe":
			result = map[string]any{
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
				"username":   "testbot",
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

	botAPI, err := api.NewBotAPIWithClient("TEST_TOKEN", fmt.Sprintf("%s/bot%%s/%%s", server.URL), server.Client())
	if err != nil {
		t.Fatalf("new test bot api: %v", err)
	}
	return botAPI
}

func TestProcessBannedMessageClearsKnownNonMember(t *testing.T) {
	t.Parallel()

	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodDeleteMessage, testTelegramMethodBanChatMember:
			return true
		case testTelegramMethodSendMessage:
			return map[string]any{
				"message_id": 700,
				"date":       0,
				"chat": map[string]any{
					"id":   100,
					"type": "supergroup",
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := &testModerationStore{}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Millisecond},
		banService: &testModerationBanService{},
	}

	msg := &api.Message{
		MessageID: 1,
		Chat:      api.Chat{ID: 100, Type: "supergroup"},
		From:      &api.User{ID: 200, UserName: "guest"},
		Text:      "spam message",
	}

	if _, err := sc.ProcessBannedMessage(context.Background(), msg, &msg.Chat, "en"); err != nil {
		t.Fatalf("ProcessBannedMessage returned error: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	if len(store.deletedKnownNonMember) != 1 {
		t.Fatalf("expected one known non-member delete, got %d", len(store.deletedKnownNonMember))
	}
	if got := store.deletedKnownNonMember[0]; got != [2]int64{100, 200} {
		t.Fatalf("unexpected delete target: %#v", got)
	}
	if store.spamCase == nil || store.spamCase.Status != spamCaseStatusSpam {
		t.Fatalf("expected spam case to be marked as spam, got %#v", store.spamCase)
	}
}

func TestProcessBannedBotMessageDeletesRecentJoinServiceMessage(t *testing.T) {
	t.Parallel()

	var deletedMessageIDs []string
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodDeleteMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get("message_id"))
			return true
		case testTelegramMethodBanChatMember:
			return true
		case testTelegramMethodSendMessage:
			return map[string]any{
				"message_id": 700,
				"date":       0,
				"chat": map[string]any{
					"id":   -100,
					"type": "group",
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := &testModerationStore{
		recentJoiners: []*db.RecentJoiner{{
			ChatID:        -100,
			UserID:        200,
			Username:      "spam_bot",
			JoinMessageID: 77,
			JoinedAt:      time.Now(),
		}},
	}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Hour},
		banService: &testModerationBanService{},
	}

	msg := &api.Message{
		MessageID: 40,
		Chat:      api.Chat{ID: -100, Type: "group"},
		From:      &api.User{ID: 200, IsBot: true, UserName: "spam_bot"},
		Text:      "spam message",
	}

	if _, err := sc.ProcessBannedMessage(context.Background(), msg, &msg.Chat, "en"); err != nil {
		t.Fatalf("ProcessBannedMessage returned error: %v", err)
	}

	if len(deletedMessageIDs) != 2 {
		t.Fatalf("expected spam message and join service message deletes, got %#v", deletedMessageIDs)
	}
	if deletedMessageIDs[0] != "40" || deletedMessageIDs[1] != "77" {
		t.Fatalf("unexpected deleted messages: %#v", deletedMessageIDs)
	}
	if len(store.processedJoiners) != 1 || store.processedJoiners[0] != [3]int64{-100, 200, 1} {
		t.Fatalf("expected spammer recent joiner to be processed, got %#v", store.processedJoiners)
	}
}

func TestResolveCaseSpamClearsKnownNonMember(t *testing.T) {
	t.Parallel()

	var deletedMessageIDs []string
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodBanChatMember:
			return true
		case testTelegramMethodDeleteMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get("message_id"))
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := &testModerationStore{
		spamCase: &db.SpamCase{
			ID:        55,
			ChatID:    100,
			UserID:    200,
			Status:    spamCaseStatusPending,
			CreatedAt: time.Now(),
		},
		votes: []*db.SpamVote{
			{Vote: false},
			{Vote: false},
			{Vote: true},
		},
		recentJoiners: []*db.RecentJoiner{{
			ChatID:        100,
			UserID:        200,
			Username:      "guest",
			JoinMessageID: 77,
			JoinedAt:      time.Now(),
		}},
	}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		store:      store,
		config:     config.SpamControl{MinVoters: 1},
		banService: &testModerationBanService{},
	}

	if err := sc.ResolveCase(context.Background(), 55, false); err != nil {
		t.Fatalf("ResolveCase returned error: %v", err)
	}

	if len(store.deletedKnownNonMember) != 1 {
		t.Fatalf("expected one known non-member delete, got %d", len(store.deletedKnownNonMember))
	}
	if got := store.deletedKnownNonMember[0]; got != [2]int64{100, 200} {
		t.Fatalf("unexpected delete target: %#v", got)
	}
	if len(deletedMessageIDs) != 1 || deletedMessageIDs[0] != "77" {
		t.Fatalf("expected join service message 77 to be deleted, got %#v", deletedMessageIDs)
	}
	if len(store.processedJoiners) != 1 || store.processedJoiners[0] != [3]int64{100, 200, 1} {
		t.Fatalf("expected spammer recent joiner to be processed, got %#v", store.processedJoiners)
	}
	if store.spamCase == nil || store.spamCase.Status != spamCaseStatusSpam {
		t.Fatalf("expected spam case to be resolved as spam, got %#v", store.spamCase)
	}
}

var _ bot.Service = (*testModerationService)(nil)
