package handlers

import (
	"context"
	"errors"
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

func TestTelegramActionAlreadyAppliedRecognizesRecoveredJoinQuery(t *testing.T) {
	t.Parallel()

	for _, message := range []string{
		"Bad Request: query is too old and response timeout expired or query ID is invalid",
		"Bad Request: QUERY_ID_INVALID",
	} {
		if !isTelegramActionAlreadyApplied(errors.New(message)) {
			t.Fatalf("expected recovered join query error to be idempotent: %q", message)
		}
	}
	if isTelegramActionAlreadyApplied(errors.New("Bad Request: chat admin required")) {
		t.Fatal("unexpected transient or permission error classified as already applied")
	}
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

func (s *testGatekeeperStore) GetChallengeByWebAppToken(context.Context, string) (*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) GetChallengeByChatUser(context.Context, int64, int64) (*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) GetPassedJoinRequestChallengeByChatUser(context.Context, int64, int64) (*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) RecordWrongAttempt(context.Context, string, int) (int, string, bool, error) {
	return 0, "", false, nil
}

func (s *testGatekeeperStore) ClaimForApproval(context.Context, string) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) BeginDMFallback(context.Context, string) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) AttachChallengeMessage(context.Context, string, string, int) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) AttachJoinMessage(context.Context, string, string, int) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) PrepareDMFallback(context.Context, string, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) CompleteExternalAction(context.Context, string, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) ScheduleChallengeRetry(context.Context, string, string, time.Time, string) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) DeleteChallengeInstance(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s *testGatekeeperStore) GetDueChallenges(context.Context, time.Time) ([]*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) GetExpiredChallenges(context.Context, time.Time) ([]*db.Challenge, error) {
	return nil, nil
}

func (s *testGatekeeperStore) MarkWebAppChallengeOpened(context.Context, string, time.Time) error {
	return nil
}

func (s *testGatekeeperStore) GetUnopenedWebAppChallenges(context.Context, time.Time) ([]*db.Challenge, error) {
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
	banned        bool
	bans          []testGatekeeperBan
	knownBanned   map[int64]bool
}

type testGatekeeperBan struct {
	chatID    int64
	userID    int64
	messageID int
}

func (c *testGatekeeperBanChecker) CheckBan(context.Context, int64) (bool, error) {
	c.checkBanCalls++
	return c.banned, nil
}

func (c *testGatekeeperBanChecker) IsKnownBanned(userID int64) bool {
	return c.knownBanned[userID]
}

func (c *testGatekeeperBanChecker) BanUserWithMessage(_ context.Context, chatID int64, userID int64, messageID int) error {
	c.bans = append(c.bans, testGatekeeperBan{
		chatID:    chatID,
		userID:    userID,
		messageID: messageID,
	})
	return nil
}

func TestProcessNewChatMembersNotSpammerOverrideBypassesBanCheck(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				"user": map[string]any{
					"id":              200,
					testJSONIsBot:     false,
					testJSONFirstName: testFirstNameUser,
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
		bot:        botAPI,
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
