package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
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
	reportMessages        []*db.SpamCaseReportMessage
	members               []int64
	membersErr            error
	presentationErr       error
	retryCalls            int
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

func (s *testModerationStore) UpdateSpamCasePresentation(_ context.Context, sc *db.SpamCase) error {
	if s.presentationErr != nil {
		return s.presentationErr
	}
	if s.spamCase == nil {
		return nil
	}
	s.spamCase.ChannelUsername = sc.ChannelUsername
	s.spamCase.ChannelPostID = sc.ChannelPostID
	s.spamCase.NotificationMessageID = sc.NotificationMessageID
	return nil
}

func (s *testModerationStore) SetSpamCasePreVoteRestricted(_ context.Context, caseID int64, restricted bool) error {
	if s.spamCase == nil || s.spamCase.ID != caseID || s.spamCase.Status != db.SpamCaseStatusPending {
		return errors.New("spam case is no longer pending")
	}
	s.spamCase.PreVoteRestricted = restricted
	return nil
}

func (s *testModerationStore) SetSpamCaseResolveAt(_ context.Context, caseID int64, resolveAt time.Time) (bool, error) {
	if s.spamCase == nil || s.spamCase.ID != caseID || s.spamCase.Status != db.SpamCaseStatusPending || s.spamCase.ResolveAt != nil {
		return false, nil
	}
	s.spamCase.ResolveAt = &resolveAt
	return true, nil
}

func (s *testModerationStore) GetSpamCase(context.Context, int64) (*db.SpamCase, error) {
	return s.spamCase, nil
}

func (s *testModerationStore) GetPendingSpamCases(context.Context) ([]*db.SpamCase, error) {
	if s.spamCase != nil && s.spamCase.Status == db.SpamCaseStatusPending {
		return []*db.SpamCase{s.spamCase}, nil
	}
	return nil, nil
}

func (s *testModerationStore) GetDueSpamCases(context.Context, time.Time) ([]*db.SpamCase, error) {
	return nil, nil
}

func (s *testModerationStore) GetActiveSpamCase(context.Context, int64, int64) (*db.SpamCase, error) {
	if s.spamCase == nil || s.spamCase.Status != spamCaseStatusPending {
		return nil, nil
	}
	return s.spamCase, nil
}

func (s *testModerationStore) GetActiveSpamCaseByMessage(_ context.Context, chatID int64, userID int64, messageID int) (*db.SpamCase, error) {
	if s.spamCase == nil || s.spamCase.Status != spamCaseStatusPending {
		return nil, nil
	}
	if s.spamCase.ChatID != chatID || s.spamCase.UserID != userID || s.spamCase.MessageID != messageID {
		return nil, nil
	}
	return s.spamCase, nil
}

func (s *testModerationStore) AddSpamVote(context.Context, *db.SpamVote) error {
	return nil
}

func (s *testModerationStore) AddVoteIfPending(_ context.Context, vote *db.SpamVote) (int, int, bool, error) {
	if s.spamCase == nil || s.spamCase.Status != db.SpamCaseStatusPending {
		return 0, 0, false, nil
	}
	s.votes = append(s.votes, vote)
	notSpam, spam := 0, 0
	for _, recorded := range s.votes {
		if recorded.Vote {
			notSpam++
		} else {
			spam++
		}
	}
	return notSpam, spam, true, nil
}

func (s *testModerationStore) GetSpamVotes(context.Context, int64) ([]*db.SpamVote, error) {
	return s.votes, nil
}

func (s *testModerationStore) ClaimSpamCaseResolution(_ context.Context, caseID int64, requiredVoters int, timedOut bool, _ time.Time) (*db.SpamCase, bool, error) {
	if s.spamCase == nil || s.spamCase.ID != caseID || s.spamCase.Status != db.SpamCaseStatusPending {
		return nil, false, nil
	}
	status, resolve := resolveStatusFromVotes(s.votes, requiredVoters, timedOut)
	if !resolve {
		return nil, false, nil
	}
	if status == db.SpamCaseStatusSpam {
		s.spamCase.Status = db.SpamCaseStatusResolvingSpam
	} else {
		s.spamCase.Status = db.SpamCaseStatusResolvingFalsePositive
	}
	copyCase := *s.spamCase
	return &copyCase, true, nil
}

func (s *testModerationStore) FinalizeSpamCaseResolution(_ context.Context, caseID int64, expectedStatus, terminalStatus, _ string, resolvedAt time.Time) (bool, error) {
	if s.spamCase == nil || s.spamCase.ID != caseID || s.spamCase.Status != expectedStatus {
		return false, nil
	}
	s.spamCase.Status = terminalStatus
	s.spamCase.ResolvedAt = &resolvedAt
	return true, nil
}

func (s *testModerationStore) ScheduleSpamCaseRetry(context.Context, int64, string, time.Time, string) (bool, error) {
	s.retryCalls++
	return true, nil
}

func (s *testModerationStore) GetMembers(context.Context, int64) ([]int64, error) {
	return s.members, s.membersErr
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

func (s *testModerationStore) AddSpamCaseReportMessage(_ context.Context, message *db.SpamCaseReportMessage) error {
	copyMessage := *message
	s.reportMessages = append(s.reportMessages, &copyMessage)
	return nil
}

func (s *testModerationStore) GetSpamCaseReportMessages(context.Context, int64) ([]*db.SpamCaseReportMessage, error) {
	return s.reportMessages, nil
}

func (s *testModerationStore) DeleteSpamCaseReportMessages(context.Context, int64) error {
	s.reportMessages = nil
	return nil
}

func (s *testModerationStore) GetDueSpamCaseReportMessages(context.Context, time.Time) ([]*db.SpamCaseReportMessage, error) {
	return s.reportMessages, nil
}

func (s *testModerationStore) DeleteSpamCaseReportMessage(_ context.Context, caseID, chatID int64, messageID int) error {
	for i, message := range s.reportMessages {
		if message.CaseID == caseID && message.ChatID == chatID && message.MessageID == messageID {
			s.reportMessages = append(s.reportMessages[:i], s.reportMessages[i+1:]...)
			break
		}
	}
	return nil
}

type testModerationBanService struct {
	muteCalls   int
	unmuteCalls int
	muteErr     error
	unmuteErr   error
}

func (s *testModerationBanService) Start(context.Context) error { return nil }
func (s *testModerationBanService) Stop(context.Context) error  { return nil }
func (s *testModerationBanService) CheckBan(context.Context, int64) (bool, error) {
	return false, nil
}

func (s *testModerationBanService) MuteUser(context.Context, int64, int64) error {
	s.muteCalls++
	return s.muteErr
}

func (s *testModerationBanService) UnmuteUser(context.Context, int64, int64) error {
	s.unmuteCalls++
	return s.unmuteErr
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
				"id":                        1,
				moderationTestJSONIsBot:     true,
				moderationTestJSONFirstName: "Test",
				"username":                  "testbot",
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

func TestProcessBannedMessageClearsKnownNonMember(t *testing.T) {
	t.Parallel()

	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodDeleteMessage, testTelegramMethodBanChatMember:
			return true
		case testTelegramMethodSendMessage:
			return map[string]any{
				moderationTestJSONMessageID: 700,
				moderationTestJSONDate:      0,
				moderationTestJSONChat: map[string]any{
					"id":                   100,
					moderationTestJSONType: moderationTestSupergroup,
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
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{SuspectNotificationTimeout: time.Millisecond},
		banService: &testModerationBanService{},
	}

	msg := &api.Message{
		MessageID: 1,
		Chat:      api.Chat{ID: 100, Type: moderationTestSupergroup},
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

func TestProcessReportedMessageStartsVotingWithoutDeletingOrMuting(t *testing.T) {
	t.Parallel()

	sendCalls := 0
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodSendMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			sendCalls++
			if got := r.Form.Get("reply_parameters"); got == "" {
				t.Fatal("expected voting prompt to reply to the reported message")
			}
			return map[string]any{
				moderationTestJSONMessageID: 700,
				moderationTestJSONDate:      0,
				moderationTestJSONChat: map[string]any{
					"id":                   100,
					moderationTestJSONType: moderationTestSupergroup,
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	store := &testModerationStore{}
	banService := &testModerationBanService{}
	sc := &SpamControl{
		s: &testModerationService{
			botAPI: botAPI,
		},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{VotingTimeoutMinutes: time.Hour, MinVoters: 1},
		banService: banService,
	}

	chat := &api.Chat{ID: 100, Type: moderationTestSupergroup}
	target := &api.Message{
		MessageID: 40,
		Chat:      *chat,
		From:      &api.User{ID: 200, FirstName: moderationTestTargetName},
		Text:      "reported text",
	}
	report := &api.Message{
		MessageID:       50,
		MessageThreadID: 7,
		Chat:            *chat,
		From:            &api.User{ID: 300, FirstName: "Reporter"},
		Text:            "/voteban",
	}

	if _, err := sc.ProcessReportedMessage(context.Background(), target, report, chat, "en"); err != nil {
		t.Fatalf("ProcessReportedMessage returned error: %v", err)
	}
	if sendCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", sendCalls)
	}
	if banService.muteCalls != 0 {
		t.Fatalf("expected report-first voting not to mute target, got %d mute calls", banService.muteCalls)
	}
	if store.spamCase == nil {
		t.Fatal("expected spam case to be created")
	}
	if store.spamCase.MessageID != target.MessageID {
		t.Fatalf("expected message-bound spam case, got %#v", store.spamCase)
	}
	if store.spamCase.PreVoteRestricted {
		t.Fatalf("expected report-first case to avoid pre-vote restriction, got %#v", store.spamCase)
	}
	if len(store.reportMessages) != 1 || store.reportMessages[0].MessageID != report.MessageID {
		t.Fatalf("expected report command artifact, got %#v", store.reportMessages)
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
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get(moderationTestJSONMessageID))
			return true
		case testTelegramMethodBanChatMember:
			return true
		case testTelegramMethodSendMessage:
			return map[string]any{
				moderationTestJSONMessageID: 700,
				moderationTestJSONDate:      0,
				moderationTestJSONChat: map[string]any{
					"id":                   -100,
					moderationTestJSONType: "group",
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
		bot:        botAPI,
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

	if len(deletedMessageIDs) != 1 {
		t.Fatalf("expected only the join service message to need an explicit delete, got %#v", deletedMessageIDs)
	}
	if deletedMessageIDs[0] != "77" {
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
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get(moderationTestJSONMessageID))
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
		bot:        botAPI,
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

func TestVoteBanReportMessageRetentionIsTenMinutes(t *testing.T) {
	t.Parallel()

	if voteBanReportMessageRetention != 10*time.Minute {
		t.Fatalf("voteBanReportMessageRetention = %v, want 10m", voteBanReportMessageRetention)
	}
}

func TestResolveReportedCaseSpamDeletesTargetAndKeepsReportMessage(t *testing.T) {
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
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get(moderationTestJSONMessageID))
			return true
		case "editMessageReplyMarkup":
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := &testModerationStore{
		spamCase: &db.SpamCase{
			ID:                    55,
			ChatID:                100,
			UserID:                200,
			MessageID:             40,
			NotificationMessageID: 70,
			Status:                spamCaseStatusPending,
			CreatedAt:             time.Now(),
			PreVoteRestricted:     false,
		},
		votes: []*db.SpamVote{
			{Vote: false},
		},
		reportMessages: []*db.SpamCaseReportMessage{{
			CaseID:    55,
			ChatID:    100,
			MessageID: 50,
			CreatedAt: time.Now(),
		}},
	}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{MinVoters: 1},
		banService: &testModerationBanService{},
	}

	if err := sc.ResolveCase(context.Background(), 55, false); err != nil {
		t.Fatalf("ResolveCase returned error: %v", err)
	}

	if len(deletedMessageIDs) != 2 || deletedMessageIDs[0] != "40" || deletedMessageIDs[1] != "70" {
		t.Fatalf("expected target and completed voting prompt deletes, got %#v", deletedMessageIDs)
	}
	if len(store.reportMessages) != 1 {
		t.Fatalf("expected report artifact to remain queued for ten-minute retention, got %#v", store.reportMessages)
	}
}

func TestResolveReportedCaseFalsePositiveKeepsTargetAndReportMessage(t *testing.T) {
	t.Parallel()

	var deletedMessageIDs []string
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodDeleteMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			deletedMessageIDs = append(deletedMessageIDs, r.Form.Get(moderationTestJSONMessageID))
			return true
		case "editMessageReplyMarkup":
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	store := &testModerationStore{
		spamCase: &db.SpamCase{
			ID:                    55,
			ChatID:                100,
			UserID:                200,
			MessageID:             40,
			NotificationMessageID: 70,
			Status:                spamCaseStatusPending,
			CreatedAt:             time.Now(),
			PreVoteRestricted:     false,
		},
		votes: []*db.SpamVote{
			{Vote: true},
		},
		reportMessages: []*db.SpamCaseReportMessage{{
			CaseID:    55,
			ChatID:    100,
			MessageID: 50,
			CreatedAt: time.Now(),
		}},
	}
	banService := &testModerationBanService{}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{MinVoters: 1},
		banService: banService,
	}

	if err := sc.ResolveCase(context.Background(), 55, false); err != nil {
		t.Fatalf("ResolveCase returned error: %v", err)
	}

	if len(deletedMessageIDs) != 1 || deletedMessageIDs[0] != "70" {
		t.Fatalf("expected only completed voting prompt delete, got %#v", deletedMessageIDs)
	}
	if banService.unmuteCalls != 0 {
		t.Fatalf("expected report-first false positive not to unmute, got %d unmute calls", banService.unmuteCalls)
	}
	if len(store.reportMessages) != 1 {
		t.Fatalf("expected report artifact to remain queued for ten-minute retention, got %#v", store.reportMessages)
	}
}

func TestRecordVoteRejectsLogChannelOutsider(t *testing.T) {
	t.Parallel()

	botAPI := newModerationTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case "getChatMember":
			return map[string]any{
				"status":               "left",
				moderationTestJSONUser: map[string]any{"id": 300, moderationTestJSONIsBot: false, moderationTestJSONFirstName: "Outsider"},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	store := &testModerationStore{spamCase: &db.SpamCase{
		ID:        55,
		ChatID:    -100,
		UserID:    200,
		Status:    db.SpamCaseStatusPending,
		CreatedAt: time.Now(),
	}}
	sc := &SpamControl{
		s:          &testModerationService{botAPI: botAPI},
		bot:        botAPI,
		store:      store,
		config:     config.SpamControl{MinVoters: 2},
		banService: &testModerationBanService{},
	}

	_, _, err := sc.RecordVote(context.Background(), 55, 300, false)
	if !errors.Is(err, ErrVoterNotEligible) {
		t.Fatalf("expected outsider rejection, got %v", err)
	}
	if len(store.votes) != 0 {
		t.Fatalf("outsider vote reached persistence: %#v", store.votes)
	}
}

func TestMembersFailureDefersResolution(t *testing.T) {
	t.Parallel()

	store := &testModerationStore{
		spamCase: &db.SpamCase{
			ID:        55,
			ChatID:    -100,
			UserID:    200,
			Status:    db.SpamCaseStatusPending,
			CreatedAt: time.Now(),
		},
		votes:      []*db.SpamVote{{Vote: false}},
		membersErr: errors.New("membership unavailable"),
	}
	sc := &SpamControl{
		s:          &testModerationService{},
		store:      store,
		config:     config.SpamControl{MinVoters: 1, MinVotersPercentage: 10},
		banService: &testModerationBanService{},
	}

	err := sc.ResolveCase(context.Background(), 55, true)
	if err == nil || !strings.Contains(err.Error(), "membership unavailable") {
		t.Fatalf("expected membership failure, got %v", err)
	}
	if store.spamCase.Status != db.SpamCaseStatusPending {
		t.Fatalf("membership failure resolved the case: %#v", store.spamCase)
	}
}

var _ bot.Service = (*testModerationService)(nil)
