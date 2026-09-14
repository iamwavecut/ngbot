package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	botservice "github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	log "github.com/sirupsen/logrus"
)

type testBotService struct {
	botAPI          *api.BotAPI
	isMember        bool
	language        string
	settings        *db.Settings
	insertedMember  int
	insertMemberErr error
}

func (s *testBotService) GetBot() *api.BotAPI {
	if s.botAPI != nil {
		return s.botAPI
	}
	return &api.BotAPI{}
}

func (s *testBotService) IsMember(context.Context, int64, int64) (bool, error) {
	return s.isMember, nil
}

func (s *testBotService) InsertMember(context.Context, int64, int64) error {
	if s.insertMemberErr != nil {
		return s.insertMemberErr
	}
	s.insertedMember++
	return nil
}

func (s *testBotService) DeleteMember(context.Context, int64, int64) error {
	return nil
}

func (s *testBotService) GetSettings(context.Context, int64) (*db.Settings, error) {
	return s.settings, nil
}

func (s *testBotService) SetSettings(context.Context, *db.Settings) error {
	return nil
}

func (s *testBotService) GetLanguage(context.Context, int64, *api.User) string {
	if s.language == "" {
		return "en"
	}
	return s.language
}

type testReactorStore struct {
	mutex          sync.Mutex
	knownNonMember bool
	upserted       []db.ChatKnownNonMember
	deleted        [][2]int64
	challenged     map[messageResultKey]int64
	recordError    error
	trusts         map[authorTrustKey]db.MessageTrust
	trustError     error
	upsertError    error
	examples       []*db.ChatSpamExample
}

func (s *testReactorStore) ListChatSpamExamples(_ context.Context, chatID int64, classification int, limit int, offset int) ([]*db.ChatSpamExample, error) {
	filtered := make([]*db.ChatSpamExample, 0, len(s.examples))
	for _, example := range s.examples {
		if example.ChatID == chatID && example.Classification == classification {
			filtered = append(filtered, example)
		}
	}
	if offset >= len(filtered) {
		return nil, nil
	}
	filtered = filtered[offset:]
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *testReactorStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return false, nil
}

func (s *testReactorStore) RecordChallengedMessage(_ context.Context, chatID int64, userID int64, messageID int) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.recordError != nil {
		return false, s.recordError
	}
	if s.challenged == nil {
		s.challenged = make(map[messageResultKey]int64)
	}
	key := messageResultKey{ChatID: chatID, MessageID: messageID}
	if _, exists := s.challenged[key]; exists {
		return false, nil
	}
	s.challenged[key] = userID
	return true, nil
}

func TestChallengeMarkerFailureDoesNotRememberAuthor(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	service := &testBotService{botAPI: botAPI}
	store := &testReactorStore{recordError: errors.New("marker write failed")}
	reactor := &Reactor{
		s:            service,
		bot:          botAPI,
		store:        store,
		spamDetector: &testSpamDetector{result: boolPtr(false)},
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	message := &api.Message{MessageID: 301, Chat: *chat, From: user, Text: testSafeFirstMessage}

	err := reactor.handleMessage(t.Context(), message, chat, user, &db.Settings{LLMFirstMessageEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "record challenged message") {
		t.Fatalf("handle message error = %v, want marker failure", err)
	}
	if service.insertedMember != 0 {
		t.Fatalf("author remembered without durable marker: %d", service.insertedMember)
	}
}

func (s *testReactorStore) IsChallengedMessage(_ context.Context, chatID int64, userID int64, messageID int) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	storedUserID, ok := s.challenged[messageResultKey{ChatID: chatID, MessageID: messageID}]
	return ok && storedUserID == userID, nil
}

func (s *testReactorStore) IsChatKnownNonMember(context.Context, int64, int64) (bool, error) {
	return s.knownNonMember, nil
}

func (s *testReactorStore) UpsertChatKnownNonMember(_ context.Context, record *db.ChatKnownNonMember) error {
	if s.upsertError != nil {
		return s.upsertError
	}
	if record != nil {
		s.upserted = append(s.upserted, *record)
		s.knownNonMember = true
	}
	return nil
}

func (s *testReactorStore) DeleteChatKnownNonMember(_ context.Context, chatID int64, userID int64) error {
	s.deleted = append(s.deleted, [2]int64{chatID, userID})
	s.knownNonMember = false
	return nil
}

type testSpamDetector struct {
	calls            int
	reportedCalls    int
	messages         []string
	reportedMessages []string
	result           *bool
	reportedResult   *bool
	err              error
	contexts         []moderation.ClassificationContext
	reportedContexts []moderation.ClassificationContext
}

func TestCheckMessageForSpamDoesNotMirrorRawContent(t *testing.T) {
	t.Parallel()

	telegramCalls := 0
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		telegramCalls++
		t.Fatalf("classification diagnostics must not call Telegram method %s", method)
		return true
	})
	reactor := &Reactor{
		bot:          botAPI,
		store:        &testReactorStore{},
		spamDetector: &testSpamDetector{err: errors.New("classification unavailable")},
		config: Config{SpamControl: config.SpamControl{
			DebugUserID: 42,
		}},
	}

	_, _ = reactor.checkMessageForSpam(t.Context(), db.DefaultSettings(1), "private-message-content")
	if telegramCalls != 0 {
		t.Fatalf("classification diagnostics made %d Telegram calls", telegramCalls)
	}
}

func TestNormalMessageClassificationTimeoutReturnsRetryableFailure(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	store := &testReactorStore{}
	reactor := &Reactor{
		s:            &testBotService{botAPI: botAPI},
		bot:          botAPI,
		store:        store,
		spamDetector: &testSpamDetector{err: context.DeadlineExceeded},
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	message := &api.Message{MessageID: 302, Chat: *chat, From: user, Text: "normal-message-secret"}

	err := reactor.handleMessage(t.Context(), message, chat, user, &db.Settings{LLMFirstMessageEnabled: true})
	failure := botservice.ClassifyUpdateFailure(err)
	if failure.Source != botservice.UpdateFailureLLM || failure.Disposition != botservice.UpdateFailureRetryable {
		t.Fatalf("classification timeout failure = %#v", failure)
	}
}

func TestMessageCapabilityLookupFailureReturnsRetryableFailure(t *testing.T) {
	t.Parallel()

	reactor := &Reactor{
		store:       &testReactorStore{},
		banService:  &testBanService{moderationErr: errors.New("telegram unavailable")},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	err := reactor.handleMessage(t.Context(), &api.Message{MessageID: 1, Chat: *chat, From: user, Text: testCandidateValue}, chat, user, &db.Settings{LLMFirstMessageEnabled: true})
	failure := botservice.ClassifyUpdateFailure(err)
	if failure.Source != botservice.UpdateFailureCapability || failure.Disposition != botservice.UpdateFailureRetryable {
		t.Fatalf("capability failure = %#v", failure)
	}
}

func TestSenderChatCapabilityLookupFailureReturnsRetryableFailure(t *testing.T) {
	t.Parallel()

	reactor := &Reactor{
		store:       &testReactorStore{},
		banService:  &testBanService{moderationErr: errors.New("telegram unavailable")},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	message := &api.Message{MessageID: 2, Chat: *chat, SenderChat: &api.Chat{ID: -200, Type: testChatTypeChannel}, Text: testCandidateValue}
	err := reactor.handleMessage(t.Context(), message, chat, nil, db.DefaultSettings(chat.ID))
	failure := botservice.ClassifyUpdateFailure(err)
	if failure.Source != botservice.UpdateFailureCapability || failure.Disposition != botservice.UpdateFailureRetryable {
		t.Fatalf("capability failure = %#v", failure)
	}
}

func TestSenderChatMalformedClassificationReturnsRetryableFailure(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(string, *http.Request) any {
		return map[string]any{"id": -100, "type": "supergroup", "linked_chat_id": -999}
	})
	reactor := &Reactor{
		bot:          botAPI,
		store:        &testReactorStore{},
		spamDetector: &testSpamDetector{err: llm.NewFailure(llm.FailureMalformedOutput, errors.New("empty"))},
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	message := &api.Message{MessageID: 3, Chat: *chat, SenderChat: &api.Chat{ID: -200, Type: testChatTypeChannel}, Text: testCandidateValue}
	err := reactor.handleMessage(t.Context(), message, chat, nil, db.DefaultSettings(chat.ID))
	failure := botservice.ClassifyUpdateFailure(err)
	if failure.Source != botservice.UpdateFailureLLM || failure.Disposition != botservice.UpdateFailureRetryable {
		t.Fatalf("classification failure = %#v", failure)
	}
}

func TestDetectedSpamActionFailurePropagates(t *testing.T) {
	t.Parallel()

	actionErr := errors.New("persistence unavailable")
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	reactor := &Reactor{
		s:            &testBotService{botAPI: botAPI},
		bot:          botAPI,
		store:        &testReactorStore{},
		spamDetector: &testSpamDetector{result: boolPtr(true)},
		banService:   &testBanService{},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			return nil, actionErr
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	message := &api.Message{MessageID: 4, Chat: *chat, From: user, Text: testSpamMessageText}
	err := reactor.handleMessage(t.Context(), message, chat, user, &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true})
	if !errors.Is(err, actionErr) {
		t.Fatalf("action error = %v, want %v", err, actionErr)
	}
}

func TestModerationRouterForwardsExhaustedLLMDegradation(t *testing.T) {
	t.Parallel()

	banService := &testBanService{}
	reactor := &Reactor{banService: banService}
	router := NewModerationRouter(nil, reactor)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	update := &api.Update{Message: &api.Message{MessageID: 5, Chat: *chat, From: user, Text: testCandidateValue}}
	failure := botservice.ClassifyUpdateFailure(botservice.NewRetryableUpdateFailure(botservice.UpdateFailureLLM, "provider", errors.New("unavailable")))
	if err := router.HandleExhaustedUpdateFailure(t.Context(), update, chat, user, failure); err != nil {
		t.Fatalf("degrade through router: %v", err)
	}
	if banService.muteCalls != 1 {
		t.Fatalf("mute calls = %d, want 1", banService.muteCalls)
	}
}

func TestExhaustedLLMFailureQuarantinesOnlyWithKnownRights(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		unavailable bool
		wantMutes   int
	}{
		{name: "known rights", wantMutes: 1},
		{name: "known no rights", unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			banService := &testBanService{moderationUnavailable: test.unavailable}
			reactor := &Reactor{banService: banService}
			chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
			user := &api.User{ID: 200}
			update := &api.Update{UpdateID: 1, Message: &api.Message{MessageID: 2, Chat: *chat, From: user, Text: testCandidateValue}}
			failure := botservice.ClassifyUpdateFailure(botservice.NewRetryableUpdateFailure(botservice.UpdateFailureLLM, "provider_error", errors.New("unavailable")))
			if err := reactor.HandleExhaustedUpdateFailure(t.Context(), update, chat, user, failure); err != nil {
				t.Fatalf("degrade exhausted LLM failure: %v", err)
			}
			if banService.muteCalls != test.wantMutes {
				t.Fatalf("mute calls = %d, want %d", banService.muteCalls, test.wantMutes)
			}
		})
	}
}

func TestClassificationFailureLogFieldsAreStructuredAndContentFree(t *testing.T) {
	t.Parallel()
	const secret = "provider-candidate-secret"

	tests := []struct {
		name    string
		err     error
		outcome llm.FailureKind
	}{
		{name: "timeout", err: context.DeadlineExceeded, outcome: llm.FailureTimeout},
		{name: "malformed", err: llm.NewFailure(llm.FailureMalformedOutput, errors.New(secret)), outcome: llm.FailureMalformedOutput},
		{name: "policy", err: llm.NewFailure(llm.FailurePolicyBlocked, errors.New(secret)), outcome: llm.FailurePolicyBlocked},
		{name: "provider", err: errors.New(secret), outcome: llm.FailureProvider},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fields := classificationFailureLogFields(tt.err, "message", "allow_message")
			if fields["llm_outcome"] != string(tt.outcome) || fields["classification_path"] != "message" || fields["fallback"] != "allow_message" {
				t.Fatalf("classification failure fields = %#v", fields)
			}
			if strings.Contains(fmt.Sprint(fields), secret) {
				t.Fatalf("classification fields leaked provider content: %#v", fields)
			}
		})
	}
}

func (d *testSpamDetector) IsSpam(_ context.Context, message string, classificationContext moderation.ClassificationContext) (*bool, error) {
	d.calls++
	d.messages = append(d.messages, message)
	d.contexts = append(d.contexts, classificationContext)
	return d.result, d.err
}

func (d *testSpamDetector) IsReportedSpam(_ context.Context, message string, classificationContext moderation.ClassificationContext) (*bool, error) {
	d.reportedCalls++
	d.reportedMessages = append(d.reportedMessages, message)
	d.reportedContexts = append(d.reportedContexts, classificationContext)
	if d.reportedResult != nil {
		return d.reportedResult, nil
	}
	if d.err != nil {
		return nil, d.err
	}
	return d.result, nil
}

func TestCheckMessageForSpamPassesProfileAndBothExampleLabels(t *testing.T) {
	t.Parallel()

	settings := db.DefaultSettings(-100)
	settings.LLMModerationProfile = db.LLMModerationProfileJobsHR
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{examples: []*db.ChatSpamExample{
		{ChatID: settings.ID, Text: "Detailed recruiter vacancy", Classification: db.SpamClassificationAllowed},
		{ChatID: settings.ID, Text: "Vague remote income offer", Classification: db.SpamClassificationSpam},
	}}
	reactor := &Reactor{store: store, spamDetector: detector}

	if _, err := reactor.checkMessageForSpam(t.Context(), settings, "candidate"); err != nil {
		t.Fatalf("check message for spam: %v", err)
	}
	if len(detector.contexts) != 1 {
		t.Fatalf("classification contexts = %d, want 1", len(detector.contexts))
	}
	classificationContext := detector.contexts[0]
	if classificationContext.Profile != db.LLMModerationProfileJobsHR {
		t.Fatalf("profile = %q, want %q", classificationContext.Profile, db.LLMModerationProfileJobsHR)
	}
	want := map[string]int{
		"Detailed recruiter vacancy": db.SpamClassificationAllowed,
		"Vague remote income offer":  db.SpamClassificationSpam,
	}
	for _, example := range classificationContext.Examples {
		if classification, ok := want[example.Message]; ok {
			if example.Classification != classification {
				t.Fatalf("example %q classification = %d, want %d", example.Message, example.Classification, classification)
			}
			delete(want, example.Message)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing classification examples: %#v", want)
	}
}

type testBanService struct {
	checkBanCalls         int
	checkBan              bool
	knownBanned           bool
	knownBannedUsers      map[int64]bool
	bans                  []testGatekeeperBan
	banDeadlines          []time.Time
	moderationUnavailable bool
	moderationErr         error
	markedUnavailable     bool
	muteCalls             int
}

func (s *testBanService) Start(context.Context) error { return nil }
func (s *testBanService) Stop(context.Context) error  { return nil }
func (s *testBanService) CheckBan(context.Context, int64) (bool, error) {
	s.checkBanCalls++
	return s.checkBan, nil
}

func (s *testBanService) ModerationAvailable(context.Context, int64) (bool, error) {
	return !s.moderationUnavailable, s.moderationErr
}

func (s *testBanService) MarkModerationUnavailable(int64) {
	s.moderationUnavailable = true
	s.markedUnavailable = true
}

func (s *testBanService) MuteUser(context.Context, int64, int64, time.Time) error {
	s.muteCalls++
	return nil
}
func (s *testBanService) UnmuteUser(context.Context, int64, int64) error { return nil }
func (s *testBanService) BanUserWithMessage(_ context.Context, chatID, userID int64, messageID int) error {
	s.bans = append(s.bans, testGatekeeperBan{chatID: chatID, userID: userID, messageID: messageID})
	return nil
}

func (s *testBanService) BanUserWithMessageUntil(ctx context.Context, chatID, userID int64, messageID int, until time.Time) error {
	s.banDeadlines = append(s.banDeadlines, until)
	return s.BanUserWithMessage(ctx, chatID, userID, messageID)
}
func (s *testBanService) UnbanUser(context.Context, int64, int64) error            { return nil }
func (s *testBanService) IsRestricted(context.Context, int64, int64) (bool, error) { return false, nil }

func (s *testBanService) IsKnownBanned(userID int64) bool {
	if s.knownBannedUsers != nil {
		return s.knownBannedUsers[userID]
	}
	return s.knownBanned
}

type testNotSpammerStore struct {
	testReactorStore
	isNotSpammer  bool
	notSpammerErr error
}

func (s *testNotSpammerStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return s.isNotSpammer, s.notSpammerErr
}

func boolPtr(value bool) *bool {
	return &value
}

func TestSenderChatUsesSharedWorkflowWithAuthoritativeIdentity(t *testing.T) {
	t.Parallel()
	for _, voting := range []bool{false, true} {
		t.Run(fmt.Sprint(voting), func(t *testing.T) {
			f := newTrustFixture(t)
			f.settings.CommunityVotingEnabled = voting
			f.detector.result = boolPtr(true)
			calls := 0
			process := func(msg *api.Message) *moderation.ProcessingResult {
				calls++
				author, ok := botservice.MessageAuthor(msg)
				if !ok || author.Kind != db.MessageAuthorSenderChat || author.ID != -200 {
					t.Fatalf("wrong author: %#v", author)
				}
				return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: !voting}
			}
			f.reactor.processSpam = func(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
				if !voting {
					t.Fatal("voting disabled but used vote workflow")
				}
				return process(msg), nil
			}
			f.reactor.processBanned = func(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
				if voting {
					t.Fatal("voting enabled but used immediate ban")
				}
				return process(msg), nil
			}
			for _, edited := range []bool{false, true} {
				msg := f.message(10 + calls)
				msg.SenderChat = &api.Chat{ID: -200, Type: "channel"}
				f.handle(t, msg, edited)
			}
			if calls != 2 || f.detector.calls != 2 {
				t.Fatalf("workflow=%d LLM=%d", calls, f.detector.calls)
			}
			banService := f.reactor.banService.(*testBanService)
			if banService.checkBanCalls != 0 || banService.muteCalls != 0 || len(banService.bans) != 0 {
				t.Fatal("technical user reached user enforcement")
			}
		})
	}
}

func TestAnonymousAdminSenderChatWithFromRemainsTrustedOnNewAndEdit(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("trusted anonymous admin reached Telegram method %q", method)
		return nil
	})
	detector := &testSpamDetector{result: boolPtr(true)}
	reactor := &Reactor{
		s: &testBotService{botAPI: botAPI}, bot: botAPI, store: &testReactorStore{}, spamDetector: detector,
		banService: &testBanService{}, lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	from := &api.User{ID: 200, FirstName: testFirstNameForwarder}
	message := &api.Message{MessageID: 620, Chat: *chat, From: from, SenderChat: chat, Text: "admin post"}
	settings := &db.Settings{LLMFirstMessageEnabled: true}
	if err := reactor.handleMessage(t.Context(), message, chat, from, settings); err != nil {
		t.Fatalf("new anonymous admin message: %v", err)
	}
	if err := reactor.handleEditedMessage(t.Context(), message, chat, from, settings); err != nil {
		t.Fatalf("edited anonymous admin message: %v", err)
	}
	if detector.calls != 0 {
		t.Fatalf("trusted anonymous admin classifier calls = %d", detector.calls)
	}
}

func TestSafeRoutedCommandIsBoundForPostGraduationEdit(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected method %q", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := &testReactorStore{}
	detector := &testSpamDetector{result: boolPtr(false)}
	service := &testBotService{botAPI: botAPI}
	reactor := &Reactor{
		s: service, bot: botAPI, store: store, spamDetector: detector, banService: &testBanService{},
		lastResults: make(map[messageResultKey]*MessageProcessingResult), now: func() time.Time { return now },
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: "User"}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}
	command := &api.Message{MessageID: 610, Chat: *chat, From: user, Text: "/settings safe"}

	if err := reactor.handleMessageChallenge(t.Context(), command, chat, user, settings, false, true); err != nil {
		t.Fatalf("moderate command: %v", err)
	}
	probation, _ := store.MessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID})
	if probation == nil || probation.TrustedUntil.Valid {
		t.Fatalf("routed probation = %#v", probation)
	}
	store.trusts[authorTrustKey{chatID: chat.ID, author: db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID}}] = db.MessageTrust{
		ChatID: chat.ID, AuthorKind: db.MessageAuthorUser, AuthorID: user.ID, SafeMessages: 3, TrustedUntil: sql.NullTime{Time: now.Add(30 * 24 * time.Hour), Valid: true},
	}
	detector.result = boolPtr(true)
	command.Text = "/settings edited spam"
	if err := reactor.handleEditedMessage(t.Context(), command, chat, user, settings); err != nil {
		t.Fatalf("moderate command edit: %v", err)
	}
	if detector.calls != 2 {
		t.Fatalf("classifier calls = %d, want 2", detector.calls)
	}
}

func TestCommandRunsProbationContentPolicyBeforeFeatureRouting(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected method %q", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	service := &testBotService{
		botAPI:   botAPI,
		settings: &db.Settings{ID: -100, LLMFirstMessageEnabled: true, CommunityVotingEnabled: true},
	}
	store := &testReactorStore{}
	processedSpam := 0
	reactor := &Reactor{
		s:            service,
		bot:          botAPI,
		store:        store,
		spamDetector: &testSpamDetector{result: boolPtr(true)},
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processedSpam++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	message := &api.Message{
		MessageID: 502,
		Chat:      *chat,
		From:      user,
		Text:      "/settings adversarial prompt",
		Entities:  []api.MessageEntity{{Type: testEntityBotCommand, Offset: 0, Length: 9}},
	}

	proceed, err := reactor.Handle(t.Context(), &api.Update{UpdateID: 77, Message: message}, chat, user)
	if err != nil {
		t.Fatalf("Handle command: %v", err)
	}
	if proceed {
		t.Fatal("spam command reached downstream feature handlers")
	}
	if processedSpam != 1 {
		t.Fatalf("spam command processing calls = %d, want 1", processedSpam)
	}
	probation, err := store.MessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID})
	if err != nil || probation == nil || probation.TrustedUntil.Valid {
		t.Fatalf("command probation = %#v, err=%v", probation, err)
	}
}

func TestFirstMessageDeletionEvasionKeepsSecondMessageUnderChallenge(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := db.DefaultSettings(chat.ID)
	settings.LLMFirstMessageEnabled = true
	service := &testBotService{botAPI: botAPI, settings: settings}
	store := &testReactorStore{}
	detector := &testSpamDetector{result: boolPtr(false)}
	processedSpam := 0
	reactor := &Reactor{
		s:            service,
		bot:          botAPI,
		store:        store,
		spamDetector: detector,
		banService:   &testBanService{},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processedSpam++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	first := &api.Message{MessageID: 300, Chat: *chat, From: user, Text: testSafeFirstMessage}
	if err := reactor.handleMessage(t.Context(), first, chat, user, settings); err != nil {
		t.Fatalf("handle safe first message: %v", err)
	}
	if detector.calls != 1 || service.insertedMember != 0 {
		t.Fatalf("first challenge calls=%d inserted=%d, want calls=1 inserted=0", detector.calls, service.insertedMember)
	}

	detector.result = boolPtr(true)
	second := &api.Message{MessageID: 301, Chat: *chat, From: user, Text: "spam after deleting first message"}
	if err := reactor.handleMessage(t.Context(), second, chat, user, settings); err != nil {
		t.Fatalf("handle spam second message: %v", err)
	}
	if detector.calls != 2 {
		t.Fatalf("second message bypassed challenge: calls=%d, want 2", detector.calls)
	}
	if processedSpam != 1 {
		t.Fatalf("spam processing calls=%d, want 1", processedSpam)
	}
	if service.insertedMember != 0 {
		t.Fatalf("spam author was trusted: inserted=%d", service.insertedMember)
	}
}

func TestEditedChallengedMessageIsRechecked(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := db.DefaultSettings(chat.ID)
	settings.LLMFirstMessageEnabled = true
	service := &testBotService{botAPI: botAPI, settings: settings}
	store := &testReactorStore{}
	detector := &testSpamDetector{result: boolPtr(false)}
	processedSpam := 0
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	reactor := &Reactor{
		s:            service,
		bot:          botAPI,
		store:        store,
		spamDetector: detector,
		banService:   &testBanService{},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processedSpam++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
		now:         func() time.Time { return now },
	}
	message := &api.Message{
		MessageID: 300,
		Chat:      *chat,
		From:      user,
		Date:      now.Unix(),
		Text:      testSafeFirstMessage,
	}

	proceed, err := reactor.Handle(t.Context(), &api.Update{Message: message}, chat, user)
	if err != nil || !proceed {
		t.Fatalf("handle first message: proceed=%t err=%v", proceed, err)
	}
	if detector.calls != 1 || service.insertedMember != 0 {
		t.Fatalf("first challenge calls=%d inserted=%d, want calls=1 inserted=0", detector.calls, service.insertedMember)
	}
	challenged, err := store.IsChallengedMessage(t.Context(), chat.ID, user.ID, message.MessageID)
	if err != nil || !challenged {
		t.Fatalf("challenged marker: challenged=%t err=%v", challenged, err)
	}

	detector.result = boolPtr(true)
	activeUnmarkedEdit := *message
	activeUnmarkedEdit.MessageID += 100
	activeUnmarkedEdit.EditDate = now.Add(time.Minute).Unix()
	activeUnmarkedEdit.Text = "spam added to rich message caption"
	proceed, err = reactor.Handle(t.Context(), &api.Update{EditedMessage: &activeUnmarkedEdit}, chat, user)
	if err != nil || !proceed {
		t.Fatalf("handle active unmarked edit: proceed=%t err=%v", proceed, err)
	}
	if detector.calls != 2 || processedSpam != 1 {
		t.Fatalf("active edit calls=%d processed=%d, want calls=2 processed=1", detector.calls, processedSpam)
	}

	detector.result = boolPtr(false)
	second := *message
	second.MessageID++
	second.Text = "safe second message"
	proceed, err = reactor.Handle(t.Context(), &api.Update{Message: &second}, chat, user)
	if err != nil || !proceed {
		t.Fatalf("handle second safe message: proceed=%t err=%v", proceed, err)
	}
	if detector.calls != 3 || service.insertedMember != 0 {
		t.Fatalf("second challenge calls=%d inserted=%d, want calls=3 inserted=0", detector.calls, service.insertedMember)
	}
	now = now.Add(3 * time.Hour)
	third := second
	third.MessageID++
	third.Text = "safe release message"
	proceed, err = reactor.Handle(t.Context(), &api.Update{Message: &third}, chat, user)
	if err != nil || !proceed {
		t.Fatalf("handle safe release message: proceed=%t err=%v", proceed, err)
	}
	if detector.calls != 4 || service.insertedMember != 1 {
		t.Fatalf("release calls=%d inserted=%d, want calls=4 inserted=1", detector.calls, service.insertedMember)
	}

	service.isMember = true
	detector.result = boolPtr(true)
	edited := *message
	edited.Date = now.Add(-time.Hour).Unix()
	edited.EditDate = now.Unix()
	edited.Text = "edited spam message"
	proceed, err = reactor.Handle(t.Context(), &api.Update{EditedMessage: &edited}, chat, user)
	if err != nil || !proceed {
		t.Fatalf("handle challenged edit: proceed=%t err=%v", proceed, err)
	}
	if detector.calls != 5 {
		t.Fatalf("LLM calls after challenged edit = %d, want 5", detector.calls)
	}
	if detector.messages[4] != edited.Text {
		t.Fatalf("edited content = %q, want %q", detector.messages[4], edited.Text)
	}
	if processedSpam != 2 {
		t.Fatalf("edited spam processing calls = %d, want 2", processedSpam)
	}
	if service.insertedMember != 1 {
		t.Fatalf("edited message repeated member insertion: %d", service.insertedMember)
	}

	unmarked := edited
	unmarked.MessageID += 20
	unmarked.Text = "unmarked edit"
	proceed, err = reactor.Handle(t.Context(), &api.Update{EditedMessage: &unmarked}, chat, user)
	if err != nil || !proceed {
		t.Fatalf("handle unmarked edit: proceed=%t err=%v", proceed, err)
	}
	if detector.calls != 5 {
		t.Fatalf("unmarked edit reached LLM: %d calls", detector.calls)
	}
}

func TestSpamVoteCallbackUsesSpamCaseChatSettings(t *testing.T) {
	t.Parallel()

	var callbackAnswers int
	var edits int
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case "getChatMember":
			return map[string]any{
				logFieldStatus: telegramMemberStatus,
				logFieldUser:   map[string]any{"id": 300, testJSONIsBot: false, testJSONFirstName: testFirstNameVoter},
			}
		case "answerCallbackQuery":
			callbackAnswers++
			return true
		case "editMessageText":
			edits++
			return map[string]any{
				logFieldMessageID: 400,
				testJSONDate:      0,
				logFieldChat: map[string]any{
					"id":         900,
					testJSONType: testChatTypeChannel,
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	targetSettings := db.DefaultSettings(-100)
	targetSettings.CommunityVotingEnabled = true
	if err := dbClient.SetSettings(ctx, targetSettings); err != nil {
		t.Fatalf("set target settings: %v", err)
	}

	spamCase, err := dbClient.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:                -100,
		UserID:                200,
		MessageText:           testSpamMessageText,
		CreatedAt:             time.Now(),
		ChannelUsername:       "log_channel",
		ChannelPostID:         400,
		NotificationMessageID: 0,
		Status:                db.SpamCaseStatusPending,
	})
	if err != nil {
		t.Fatalf("create spam case: %v", err)
	}

	service := botservice.NewService(ctx, botAPI, dbClient, "en", log.NewEntry(log.New()))
	spamControl := moderation.NewSpamControl(service, botAPI, dbClient, config.SpamControl{
		MinVoters:            2,
		MaxVoters:            10,
		MinVotersPercentage:  0,
		VotingTimeoutMinutes: time.Minute,
	}, &testBanService{}, false)
	reactor := NewReactor(service, botAPI, dbClient, dbClient, &testBanService{}, spamControl, nil, Config{})

	logChat := &api.Chat{ID: 900, Type: testChatTypeChannel}
	voter := &api.User{ID: 300, FirstName: testFirstNameVoter}
	update := &api.Update{
		CallbackQuery: &api.CallbackQuery{
			ID:   "callback-id",
			From: voter,
			Data: "spam_vote:" + strconv.FormatInt(spamCase.ID, 10) + ":1",
			Message: &api.Message{
				MessageID: 400,
				Chat:      *logChat,
				ReplyMarkup: &api.InlineKeyboardMarkup{
					InlineKeyboard: [][]api.InlineKeyboardButton{api.NewInlineKeyboardRow(
						api.NewInlineKeyboardButtonData("Spam", "spam_vote:1:1"),
					)},
				},
			},
		},
	}

	proceed, err := reactor.Handle(ctx, update, logChat, voter)
	if err != nil {
		t.Fatalf("handle callback: %v", err)
	}
	if !proceed {
		t.Fatal("expected callback handler to proceed")
	}
	if callbackAnswers != 1 || edits != 1 {
		t.Fatalf("expected callback answer and edit, got answers=%d edits=%d", callbackAnswers, edits)
	}

	votes, err := dbClient.GetSpamVotes(ctx, spamCase.ID)
	if err != nil {
		t.Fatalf("get spam votes: %v", err)
	}
	if len(votes) != 1 || votes[0].VoterID != voter.ID || votes[0].Vote {
		t.Fatalf("unexpected votes: %#v", votes)
	}

	resolvedAt := time.Now()
	spamCase.Status = db.SpamCaseStatusSpam
	spamCase.ResolvedAt = &resolvedAt
	if err := dbClient.UpdateSpamCase(ctx, spamCase); err != nil {
		t.Fatalf("close spam case: %v", err)
	}
	proceed, err = reactor.Handle(ctx, update, logChat, voter)
	if err != nil || !proceed {
		t.Fatalf("handle stale callback: proceed=%t err=%v", proceed, err)
	}
	if callbackAnswers != 2 || edits != 1 {
		t.Fatalf("stale callback was not acknowledged quietly: answers=%d edits=%d", callbackAnswers, edits)
	}
}

func TestSpamVoteHandlerChainConsumesBanlistPrecheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		logChatID     int64
		targetChatID  int64
		allowlistChat int64
		wantChecks    int
	}{
		{name: "same target normal voter", logChatID: -100, targetChatID: -100, wantChecks: 1},
		{name: "same target allowlisted voter", logChatID: -100, targetChatID: -100, allowlistChat: -100, wantChecks: 0},
		{name: "log allowlist does not authorize target", logChatID: 900, targetChatID: -100, allowlistChat: 900, wantChecks: 1},
		{name: "target allowlist wins across log chat", logChatID: 900, targetChatID: -100, allowlistChat: -100, wantChecks: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				switch method {
				case testTelegramMethodGetChatMember:
					return testChatMemberResponse(telegramMemberStatus, false, false, false)
				case "answerCallbackQuery":
					return true
				case "editMessageText":
					return map[string]any{logFieldMessageID: 400, testJSONDate: 0, logFieldChat: map[string]any{"id": tt.logChatID, testJSONType: testChatTypeChannel}}
				default:
					t.Fatalf("unexpected bot method: %s", method)
					return nil
				}
			})
			ctx := t.Context()
			dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
			if err != nil {
				t.Fatalf("new sqlite client: %v", err)
			}
			t.Cleanup(func() { _ = dbClient.Close() })
			if tt.allowlistChat != 0 {
				if _, err := dbClient.CreateChatNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
					ChatID: tt.allowlistChat, MatchType: db.NotSpammerMatchTypeUserID, MatchValue: "300", CreatedByUserID: 1,
				}); err != nil {
					t.Fatalf("create allowlist: %v", err)
				}
			}
			settings := db.DefaultSettings(tt.targetChatID)
			if err := dbClient.SetSettings(ctx, settings); err != nil {
				t.Fatalf("set settings: %v", err)
			}
			spamCase, err := dbClient.CreateSpamCase(ctx, &db.SpamCase{
				ChatID: tt.targetChatID, UserID: 200, MessageID: 40, MessageText: testSpamMessageText, CreatedAt: time.Now(), Status: db.SpamCaseStatusPending,
			})
			if err != nil {
				t.Fatalf("create spam case: %v", err)
			}
			service := botservice.NewService(ctx, botAPI, dbClient, "en", log.NewEntry(log.New()))
			banService := &testBanService{}
			spamControl := moderation.NewSpamControl(service, botAPI, dbClient, config.SpamControl{
				MinVoters: 2, MaxVoters: 10, VotingTimeoutMinutes: time.Minute,
			}, banService, false)
			reactor := NewReactor(service, botAPI, dbClient, dbClient, banService, spamControl, nil, Config{})
			features := NewReactorFeatures(reactor)
			router := NewModerationRouter(NewBanlistGuard(botAPI, dbClient, banService), reactor, features)
			processor := botservice.NewUpdateProcessor(service, router, features)
			logChat := api.Chat{ID: tt.logChatID, Type: testChatTypeChannel}
			voter := api.User{ID: 300, UserName: "voter_name", FirstName: testFirstNameVoter}
			update := &api.Update{CallbackQuery: &api.CallbackQuery{
				ID: "callback-id", From: &voter, Data: "spam_vote:" + strconv.FormatInt(spamCase.ID, 10) + ":1",
				Message: &api.Message{MessageID: 400, Chat: logChat},
			}}
			if err := processor.Process(ctx, update); err != nil {
				t.Fatalf("process callback: %v", err)
			}
			if banService.checkBanCalls != tt.wantChecks {
				t.Fatalf("provider checks = %d, want %d", banService.checkBanCalls, tt.wantChecks)
			}
			votes, err := dbClient.GetSpamVotes(ctx, spamCase.ID)
			if err != nil || len(votes) != 1 {
				t.Fatalf("target vote result = %#v, err %v", votes, err)
			}
		})
	}
}

func TestExternalReplyUsesLLMAndCanBeSafe(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(string, *http.Request) any {
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	service := &testBotService{language: "ru", botAPI: botAPI}
	detector := &testSpamDetector{result: boolPtr(false)}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   &testBanService{},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processSpamCalls++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	msg := &api.Message{
		MessageID: 1,
		Chat:      *chat,
		From:      user,
		Text:      "попробуйте работает",
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: testChatTypeChannel},
		},
		Quote: &api.TextQuote{Text: "цитата"},
	}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if detector.calls != 1 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if processSpamCalls != 0 {
		t.Fatalf("expected processSpam to be called once, got %d", processSpamCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.IsSpam == nil || *result.IsSpam {
		t.Fatalf("expected spam result, got %#v", result.IsSpam)
	}
	if result.SkipReason != "" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageCleanLeftUserRememberedAsKnownNonMember(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				logFieldUser: map[string]any{
					"id":              200,
					testJSONIsBot:     false,
					testJSONFirstName: testFirstNameUser,
				},
				logFieldStatus: testMemberStatusLeft,
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI}
	store := &testReactorStore{}
	detector := &testSpamDetector{result: boolPtr(false)}
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        store,
		spamDetector: detector,
		banService:   &testBanService{},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processSpam should not be called")
			return nil, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
		now:         func() time.Time { return now },
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	msg := &api.Message{MessageID: 11, Chat: *chat, From: user, Text: testMessageText}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if len(store.upserted) != 0 {
		t.Fatalf("first safe message ended probation: upserted=%d", len(store.upserted))
	}
	second := *msg
	second.MessageID++
	now = now.Add(3 * time.Hour)
	if err := r.handleMessage(context.Background(), &second, chat, user, settings); err != nil {
		t.Fatalf("handle second message: %v", err)
	}

	third := second
	third.MessageID++
	if err := r.handleMessage(t.Context(), &third, chat, user, settings); err != nil {
		t.Fatal(err)
	}

	if detector.calls != 3 {
		t.Fatalf("expected LLM detector to be called twice, got %d", detector.calls)
	}
	if service.insertedMember != 0 {
		t.Fatalf("expected member insertion to be skipped, got %d", service.insertedMember)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected one known non-member upsert, got %d", len(store.upserted))
	}
	if store.upserted[0].ChatID != chat.ID || store.upserted[0].UserID != user.ID {
		t.Fatalf("unexpected known non-member upsert: %#v", store.upserted[0])
	}
}

func TestHandleMessageAdminRequiredDuringAuthorLookupDoesNotFailUpdate(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodGetChatMember {
			return &testBotAPIError{code: http.StatusBadRequest, description: "Bad Request: CHAT_ADMIN_REQUIRED"}
		}
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	service := &testBotService{botAPI: botAPI}
	store := &testReactorStore{}
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        store,
		spamDetector: &testSpamDetector{result: boolPtr(false)},
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	msg := &api.Message{MessageID: 12, Chat: *chat, From: user, Text: testMessageText}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	second := *msg
	second.MessageID++
	if err := r.handleMessage(context.Background(), &second, chat, user, settings); err != nil {
		t.Fatalf("handle second message: %v", err)
	}
	if service.insertedMember != 0 || len(store.upserted) != 0 {
		t.Fatalf("author state changed without a membership lookup: inserted=%d upserted=%#v", service.insertedMember, store.upserted)
	}
}

func TestHandleMessageNotSpammerOverrideBypassesBanAndLLM(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				logFieldUser: map[string]any{
					"id":              200,
					testJSONIsBot:     false,
					testJSONFirstName: testFirstNameUser,
				},
				logFieldStatus: telegramMemberStatus,
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI, language: "ru"}
	detector := &testSpamDetector{}
	banService := &testBanService{knownBanned: true, checkBan: true}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testNotSpammerStore{isNotSpammer: true},
		spamDetector: detector,
		banService:   banService,
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processSpamCalls++
			return nil, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, UserName: "override_user"}
	msg := &api.Message{
		MessageID: 10,
		Chat:      *chat,
		From:      user,
		Text:      testMessageText,
	}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if banService.checkBanCalls != 0 {
		t.Fatalf("expected manual override before every banlist lookup, got %d calls", banService.checkBanCalls)
	}
	if len(banService.bans) != 0 {
		t.Fatalf("expected no direct bans for manually allowlisted user, got %#v", banService.bans)
	}
	if processSpamCalls != 0 {
		t.Fatalf("expected processSpam not to be called, got %d calls", processSpamCalls)
	}
	if service.insertedMember != 1 {
		t.Fatalf("expected member insertion, got %d", service.insertedMember)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.Stage != StageOverrideCheck {
		t.Fatalf("unexpected stage: %s", result.Stage)
	}
	if result.SkipReason != "User is manually marked as not spammer" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageWithoutModerationRightsSkipsAllSpamChecks(t *testing.T) {
	t.Parallel()

	service := &testBotService{}
	detector := &testSpamDetector{}
	banService := &testBanService{moderationUnavailable: true}
	processSpamCalls := 0
	reactor := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   banService,
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processSpamCalls++
			return nil, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, UserName: "new_user"}
	msg := &api.Message{MessageID: 10, Chat: *chat, From: user, Text: testMessageText}

	if err := reactor.handleMessage(context.Background(), msg, chat, user, &db.Settings{LLMFirstMessageEnabled: true}); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if detector.calls != 0 || banService.checkBanCalls != 0 || processSpamCalls != 0 {
		t.Fatalf("no-rights chat reached spam checks: llm=%d ban=%d moderation=%d", detector.calls, banService.checkBanCalls, processSpamCalls)
	}
	probation, err := reactor.store.MessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID})
	if err != nil || probation != nil {
		t.Fatalf("no-rights message created probation: probation=%#v err=%v", probation, err)
	}
	result := reactor.GetLastProcessingResult(chat.ID, msg.MessageID)
	if result == nil || !result.Skipped || result.SkipReason != messageSkipReasonNoModerationRights {
		t.Fatalf("unexpected no-rights processing result: %#v", result)
	}
}

func TestHandleMessageChatAdministratorBypassesBanAndLLM(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChatMember:
			if got := r.Form.Get("user_id"); got != "200" {
				t.Fatalf("expected admin member lookup for user 200, got %q", got)
			}
			return testChatMemberResponse("administrator", false, false, false)
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI}
	detector := &testSpamDetector{result: boolPtr(true)}
	banService := &testBanService{}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   banService,
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processSpamCalls++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameAdmin}
	msg := &api.Message{MessageID: 14, Chat: *chat, From: user, Text: "реклама от админа"}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if banService.checkBanCalls != 1 {
		t.Fatalf("expected banlist to be checked before the admin exemption, got %d calls", banService.checkBanCalls)
	}
	if processSpamCalls != 0 {
		t.Fatalf("expected processSpam not to be called, got %d calls", processSpamCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != "User is chat administrator" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageLinkedChannelSenderBypassesSpamPipeline(t *testing.T) {
	t.Parallel()

	getChatCalls := 0
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChat:
			getChatCalls++
			if got := r.Form.Get("chat_id"); got != "-100" {
				t.Fatalf("expected linked group lookup, got chat_id %q", got)
			}
			return map[string]any{
				"id":                 -100,
				testJSONType:         testChatTypeSupergroup,
				testJSONTitle:        "Discussion",
				testJSONLinkedChatID: -200,
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI}
	detector := &testSpamDetector{result: boolPtr(true)}
	banService := &testBanService{checkBan: true}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   banService,
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processSpamCalls++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	msg := &api.Message{
		MessageID: 15,
		Chat:      *chat,
		From:      &api.User{ID: 200, FirstName: testFirstNameForwarder},
		SenderChat: &api.Chat{
			ID:    -200,
			Type:  testChatTypeChannel,
			Title: "Linked Channel",
		},
		Text: "рекламный пост связанного канала",
	}

	for _, update := range []*api.Update{{Message: msg}, {EditedMessage: msg}} {
		proceed, err := r.Handle(context.Background(), update, chat, msg.From)
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
		if !proceed {
			t.Fatal("expected reactor to proceed")
		}
	}

	if getChatCalls != 1 {
		t.Fatalf("expected one cached getChat lookup, got %d", getChatCalls)
	}
	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if banService.checkBanCalls != 0 {
		t.Fatalf("expected ban check not to be called, got %d calls", banService.checkBanCalls)
	}
	if processSpamCalls != 0 {
		t.Fatalf("expected processSpam not to be called, got %d calls", processSpamCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != "Linked channel sender" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageSenderChatLookupFailureIsRetryable(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodGetChat {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return &testBotAPIError{code: http.StatusBadGateway, description: "temporary upstream failure"}
	})
	service := &testBotService{botAPI: botAPI}
	reactor := &Reactor{
		s: service, bot: botAPI, store: &testReactorStore{}, spamDetector: &testSpamDetector{},
		banService: &testBanService{}, lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	message := &api.Message{MessageID: 16, Chat: *chat, SenderChat: &api.Chat{ID: -200, Type: testChatTypeChannel}, Text: testCandidateValue}
	_, err := reactor.Handle(t.Context(), &api.Update{Message: message}, chat, nil)
	failure := botservice.ClassifyUpdateFailure(err)
	if failure.Disposition != botservice.UpdateFailureRetryable || failure.Source != botservice.UpdateFailureTelegram {
		t.Fatalf("failure = %#v, want retryable Telegram classification", failure)
	}
}

func TestHandleMessageKnownNonMemberDoesNotBypassMessageProbation(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				logFieldUser: map[string]any{
					"id":              200,
					testJSONIsBot:     false,
					testJSONFirstName: testFirstNameUser,
				},
				logFieldStatus: testMemberStatusLeft,
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI}
	store := &testReactorStore{knownNonMember: true}
	detector := &testSpamDetector{result: boolPtr(false)}
	banService := &testBanService{}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        store,
		spamDetector: detector,
		banService:   banService,
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			processSpamCalls++
			return nil, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	msg := &api.Message{
		MessageID: 12,
		Chat:      *chat,
		From:      user,
		Text:      "reply from guest",
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: testChatTypeChannel},
		},
	}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if detector.calls != 1 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if processSpamCalls != 0 {
		t.Fatalf("known non-member bypassed spam processing: got %d calls", processSpamCalls)
	}
	if banService.checkBanCalls != 1 {
		t.Fatalf("expected ban check to run before bypass, got %d calls", banService.checkBanCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != "" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
	probation, err := store.MessageTrust(t.Context(), chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID})
	if err != nil || probation == nil {
		t.Fatalf("known non-member probation: probation=%#v err=%v", probation, err)
	}
}

func TestProcessingResultsAreScopedByChat(t *testing.T) {
	t.Parallel()

	r := &Reactor{lastResults: make(map[messageResultKey]*MessageProcessingResult)}
	first := &MessageProcessingResult{SkipReason: "first"}
	second := &MessageProcessingResult{SkipReason: "second"}

	r.storeLastResult(-1001, 7, first)
	r.storeLastResult(-1002, 7, second)

	if got := r.GetLastProcessingResult(-1001, 7); got != first {
		t.Fatalf("first chat result = %#v", got)
	}
	if got := r.GetLastProcessingResult(-1002, 7); got != second {
		t.Fatalf("second chat result = %#v", got)
	}
}

func TestHandleMessageWithoutUserOrSenderChatSkipsSafely(t *testing.T) {
	t.Parallel()

	r := &Reactor{lastResults: make(map[messageResultKey]*MessageProcessingResult)}
	chat := &api.Chat{ID: -1001, Type: testChatTypeSupergroup}
	msg := &api.Message{MessageID: 7, Chat: *chat, Text: "anonymous"}

	if err := r.handleMessage(context.Background(), msg, chat, nil, db.DefaultSettings(chat.ID)); err != nil {
		t.Fatalf("handle anonymous message: %v", err)
	}
	result := r.GetLastProcessingResult(chat.ID, msg.MessageID)
	if result == nil || !result.Skipped || result.SkipReason != messageSkipReasonAnonymousSender {
		t.Fatalf("expected safe anonymous sender skip, got %#v", result)
	}
}

func TestHandleMessageKnownBannedMemberIsDirectlyBanned(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodDeleteMessage {
			return true
		}
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	service := &testBotService{botAPI: botAPI, isMember: true}
	detector := &testSpamDetector{}
	banService := &testBanService{knownBanned: true}
	reactor := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   banService,
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("banlisted messages must not create moderation cases")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	msg := &api.Message{MessageID: 15, Chat: *chat, From: user, Text: "known banned member message"}

	if err := reactor.handleMessage(context.Background(), msg, chat, user, &db.Settings{LLMFirstMessageEnabled: true}); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if len(banService.bans) != 1 {
		t.Fatalf("expected one direct ban, got %#v", banService.bans)
	}
	if banService.bans[0].chatID != chat.ID || banService.bans[0].userID != user.ID || banService.bans[0].messageID != msg.MessageID {
		t.Fatalf("unexpected direct ban: %#v", banService.bans[0])
	}
	if banService.checkBanCalls != 0 {
		t.Fatalf("expected in-memory banlist lookup without online checks, got %d calls", banService.checkBanCalls)
	}
	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}

	result := reactor.GetLastProcessingResult(chat.ID, msg.MessageID)
	if result == nil || !result.Skipped || result.SkipReason != "User is banned" {
		t.Fatalf("unexpected processing result: %#v", result)
	}
	if !result.Actions.MessageDeleted || !result.Actions.UserBanned {
		t.Fatalf("expected destructive moderation actions, got %#v", result.Actions)
	}
}

func TestHandleMessageCleanMemberInsertsMemberInsteadOfKnownNonMember(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				logFieldUser: map[string]any{
					"id":              200,
					testJSONIsBot:     false,
					testJSONFirstName: testFirstNameUser,
				},
				logFieldStatus: telegramMemberStatus,
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI}
	store := &testReactorStore{}
	detector := &testSpamDetector{result: boolPtr(false)}
	r := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        store,
		spamDetector: detector,
		banService:   &testBanService{},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processSpam should not be called")
			return nil, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			t.Fatal("processBanned should not be called")
			return nil, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
		now:         func() time.Time { return now },
	}

	chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	msg := &api.Message{MessageID: 13, Chat: *chat, From: user, Text: testMessageText}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if service.insertedMember != 0 {
		t.Fatalf("first safe message ended probation: inserted=%d", service.insertedMember)
	}
	second := *msg
	second.MessageID++
	now = now.Add(3 * time.Hour)
	if err := r.handleMessage(context.Background(), &second, chat, user, settings); err != nil {
		t.Fatalf("handle second message: %v", err)
	}

	third := second
	third.MessageID++
	if err := r.handleMessage(t.Context(), &third, chat, user, settings); err != nil {
		t.Fatal(err)
	}

	if service.insertedMember != 1 {
		t.Fatalf("expected member insertion, got %d", service.insertedMember)
	}
	if len(store.upserted) != 0 {
		t.Fatalf("expected no known non-member upsert, got %d", len(store.upserted))
	}
}

func TestReplyAndForwardFormsUseLLM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  *api.Message
	}{
		{
			name: "local reply only",
			msg: &api.Message{
				MessageID:      3,
				Text:           "обычный ответ",
				ReplyToMessage: &api.Message{MessageID: 30, Text: "локальное сообщение"},
			},
		},
		{
			name: "quote without external reply",
			msg: &api.Message{
				MessageID: 4,
				Text:      "цитирую",
				Quote:     &api.TextQuote{Text: "кусок сообщения"},
			},
		},
		{
			name: "via bot without external reply",
			msg: &api.Message{
				MessageID: 5,
				Text:      "через бота",
				ViaBot:    &api.User{ID: 77, IsBot: true},
			},
		},
		{
			name: "forward origin without external reply",
			msg: &api.Message{
				MessageID:     6,
				Text:          "форвард",
				ForwardOrigin: &api.MessageOrigin{Type: api.MessageOriginChannel},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			botAPI := newTestBotAPI(t, func(string, *http.Request) any {
				return testChatMemberResponse(telegramMemberStatus, false, false, false)
			})
			service := &testBotService{botAPI: botAPI}
			detector := &testSpamDetector{result: boolPtr(false)}
			processSpamCalls := 0
			r := &Reactor{
				s:            service,
				bot:          service.GetBot(),
				store:        &testReactorStore{},
				spamDetector: detector,
				banService:   &testBanService{},
				processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
					processSpamCalls++
					return nil, nil
				},
				processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
					return nil, nil
				},
				lastResults: make(map[messageResultKey]*MessageProcessingResult),
			}

			chat := &api.Chat{ID: 100, Type: testChatTypeSupergroup}
			user := &api.User{ID: 200}
			msg := tt.msg
			msg.Chat = *chat
			msg.From = user
			settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

			if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
				t.Fatalf("handleMessage returned error: %v", err)
			}

			if detector.calls != 1 {
				t.Fatalf("expected LLM detector to be called once, got %d", detector.calls)
			}
			if processSpamCalls != 0 {
				t.Fatalf("expected processSpam not to be called, got %d", processSpamCalls)
			}
		})
	}
}
