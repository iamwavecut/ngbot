package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

type testGatekeeperStore struct {
	joiners      []*db.RecentJoiner
	processed    []testProcessedJoiner
	isNotSpammer bool
}

type testProcessedJoiner struct {
	chatID    int64
	userID    int64
	isSpammer bool
}

func (s *testGatekeeperStore) CreateChallenge(context.Context, *db.Challenge) (*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) GetChallengeByMessage(context.Context, int64, int64, int) (*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) GetChallengeByChatUser(context.Context, int64, int64) (*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) UpdateChallenge(context.Context, *db.Challenge) error {
	return nil
}

func (s *testGatekeeperStore) DeleteChallenge(context.Context, int64, int64, int64) error {
	return nil
}

func (s *testGatekeeperStore) GetExpiredChallenges(context.Context, time.Time) ([]*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) AddChatRecentJoiner(context.Context, *db.RecentJoiner) (*db.RecentJoiner, error) {
	return nil, nil
}

func (s *testGatekeeperStore) GetUnprocessedRecentJoiners(context.Context) ([]*db.RecentJoiner, error) {
	return s.joiners, nil
}

func (s *testGatekeeperStore) ProcessRecentJoiner(_ context.Context, chatID int64, userID int64, isSpammer bool) error {
	s.processed = append(s.processed, testProcessedJoiner{
		chatID:    chatID,
		userID:    userID,
		isSpammer: isSpammer,
	})
	return nil
}

func (s *testGatekeeperStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return s.isNotSpammer, nil
}

type testGatekeeperBanChecker struct {
	checkBanCalls int
}

func (c *testGatekeeperBanChecker) CheckBan(context.Context, int64) (bool, error) {
	c.checkBanCalls++
	return false, nil
}

func (c *testGatekeeperBanChecker) IsKnownBanned(int64) bool {
	return false
}

func (c *testGatekeeperBanChecker) BanUserWithMessage(context.Context, int64, int64, int) error {
	return nil
}

func TestProcessNewChatMembersNotSpammerOverrideBypassesBanCheck(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case "getChatMember":
			return map[string]any{
				"user": map[string]any{
					"id":         200,
					"is_bot":     false,
					"first_name": "User",
				},
				"status": "member",
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := &testGatekeeperStore{
		joiners: []*db.RecentJoiner{
			{
				ChatID:   100,
				UserID:   200,
				Username: "override_user",
			},
		},
		isNotSpammer: true,
	}
	banChecker := &testGatekeeperBanChecker{}
	gatekeeper := &Gatekeeper{
		s:          &testBotService{botAPI: botAPI},
		store:      store,
		config:     &config.Config{},
		banChecker: banChecker,
	}

	if err := gatekeeper.processNewChatMembers(context.Background()); err != nil {
		t.Fatalf("processNewChatMembers returned error: %v", err)
	}

	if banChecker.checkBanCalls != 0 {
		t.Fatalf("expected ban checker not to be called, got %d calls", banChecker.checkBanCalls)
	}
	if len(store.processed) != 1 {
		t.Fatalf("expected one processed joiner, got %d", len(store.processed))
	}
	if store.processed[0].isSpammer {
		t.Fatal("expected overridden joiner to be processed as not spammer")
	}
}
