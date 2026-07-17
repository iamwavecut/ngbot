package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
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
	probations     map[messageProbationKey]db.MessageProbation
	probationError error
	graduateError  error
	upsertError    error
}

type messageProbationKey struct {
	chatID int64
	userID int64
}

func (s *testReactorStore) ListChatSpamExamples(context.Context, int64, int, int) ([]*db.ChatSpamExample, error) {
	return nil, nil
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
	message := &api.Message{MessageID: 301, Chat: *chat, From: user, Text: "safe first message"}

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

func (s *testReactorStore) MessageProbation(_ context.Context, chatID int64, userID int64) (*db.MessageProbation, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.probationError != nil {
		return nil, s.probationError
	}
	probation, ok := s.probations[messageProbationKey{chatID: chatID, userID: userID}]
	if !ok {
		return nil, nil
	}
	return &probation, nil
}

func (s *testReactorStore) GetOrCreateMessageProbation(_ context.Context, chatID int64, userID int64, startedAt time.Time, eligibleAt time.Time) (*db.MessageProbation, bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.probationError != nil {
		return nil, false, s.probationError
	}
	if s.probations == nil {
		s.probations = make(map[messageProbationKey]db.MessageProbation)
	}
	key := messageProbationKey{chatID: chatID, userID: userID}
	probation, ok := s.probations[key]
	if !ok {
		probation = db.MessageProbation{ChatID: chatID, UserID: userID, StartedAt: startedAt, EligibleAt: eligibleAt}
		s.probations[key] = probation
	}
	return &probation, !ok, nil
}

func (s *testReactorStore) MarkMessageProbationGraduated(_ context.Context, chatID int64, userID int64, graduatedAt time.Time) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.graduateError != nil {
		return s.graduateError
	}
	key := messageProbationKey{chatID: chatID, userID: userID}
	probation, ok := s.probations[key]
	if !ok {
		return errors.New("probation not found")
	}
	probation.GraduatedAt.Time = graduatedAt
	probation.GraduatedAt.Valid = true
	s.probations[key] = probation
	return nil
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
}

func (d *testSpamDetector) IsSpam(_ context.Context, message string, _ []string) (*bool, error) {
	d.calls++
	d.messages = append(d.messages, message)
	return d.result, d.err
}

func (d *testSpamDetector) IsReportedSpam(_ context.Context, message string, _ []string) (*bool, error) {
	d.reportedCalls++
	d.reportedMessages = append(d.reportedMessages, message)
	if d.reportedResult != nil {
		return d.reportedResult, nil
	}
	return d.result, nil
}

type testBanService struct {
	checkBanCalls         int
	checkBan              bool
	knownBanned           bool
	bans                  []testGatekeeperBan
	moderationUnavailable bool
	markedUnavailable     bool
}

func (s *testBanService) Start(context.Context) error { return nil }
func (s *testBanService) Stop(context.Context) error  { return nil }
func (s *testBanService) CheckBan(context.Context, int64) (bool, error) {
	s.checkBanCalls++
	return s.checkBan, nil
}

func (s *testBanService) ModerationAvailable(context.Context, int64) (bool, error) {
	return !s.moderationUnavailable, nil
}

func (s *testBanService) MarkModerationUnavailable(int64) {
	s.moderationUnavailable = true
	s.markedUnavailable = true
}
func (s *testBanService) MuteUser(context.Context, int64, int64) error   { return nil }
func (s *testBanService) UnmuteUser(context.Context, int64, int64) error { return nil }
func (s *testBanService) BanUserWithMessage(_ context.Context, chatID, userID int64, messageID int) error {
	s.bans = append(s.bans, testGatekeeperBan{chatID: chatID, userID: userID, messageID: messageID})
	return nil
}
func (s *testBanService) UnbanUser(context.Context, int64, int64) error            { return nil }
func (s *testBanService) IsRestricted(context.Context, int64, int64) (bool, error) { return false, nil }

func (s *testBanService) IsKnownBanned(int64) bool { return s.knownBanned }

type testNotSpammerStore struct {
	testReactorStore
	isNotSpammer bool
}

func (s *testNotSpammerStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return s.isNotSpammer, nil
}

func boolPtr(value bool) *bool {
	return &value
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

	first := &api.Message{MessageID: 300, Chat: *chat, From: user, Text: "safe first message"}
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
		Text:      "safe first message",
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
				logFieldUser:   map[string]any{"id": 300, testJSONIsBot: false, testJSONFirstName: "Voter"},
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
		MessageText:           "spam",
		CreatedAt:             time.Now(),
		ChannelUsername:       "log_channel",
		ChannelPostID:         400,
		NotificationMessageID: 0,
		Status:                "pending",
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
	voter := &api.User{ID: 300, FirstName: "Voter"}
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

func TestHandleMessageExternalQuoteHeuristic(t *testing.T) {
	t.Parallel()

	service := &testBotService{language: "ru"}
	detector := &testSpamDetector{}
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

	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if processSpamCalls != 1 {
		t.Fatalf("expected processSpam to be called once, got %d", processSpamCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.IsSpam == nil || !*result.IsSpam {
		t.Fatalf("expected spam result, got %#v", result.IsSpam)
	}
	if result.SkipReason != messageSkipReasonExternalQuote {
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

	if detector.calls != 2 {
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
	banService := &testBanService{}
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
	if banService.checkBanCalls != 1 {
		t.Fatalf("expected banlist to be checked before the override, got %d calls", banService.checkBanCalls)
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
	probation, err := reactor.store.MessageProbation(t.Context(), chat.ID, user.ID)
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
				"id":             -100,
				testJSONType:     testChatTypeSupergroup,
				testJSONTitle:    "Discussion",
				"linked_chat_id": -200,
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
		SenderChat: &api.Chat{
			ID:    -200,
			Type:  testChatTypeChannel,
			Title: "Linked Channel",
		},
		Text: "рекламный пост связанного канала",
	}

	proceed, err := r.Handle(context.Background(), &api.Update{Message: msg}, chat, nil)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !proceed {
		t.Fatal("expected reactor to proceed")
	}

	if getChatCalls != 1 {
		t.Fatalf("expected one getChat call, got %d", getChatCalls)
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

	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if processSpamCalls != 1 {
		t.Fatalf("known non-member bypassed spam processing: got %d calls", processSpamCalls)
	}
	if banService.checkBanCalls != 1 {
		t.Fatalf("expected ban check to run before bypass, got %d calls", banService.checkBanCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != messageSkipReasonExternalQuote {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
	probation, err := store.MessageProbation(t.Context(), chat.ID, user.ID)
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

func TestHandleMessageExternalQuoteHeuristicDoesNotTriggerForNonFirstMessage(t *testing.T) {
	t.Parallel()

	service := &testBotService{isMember: true}
	detector := &testSpamDetector{}
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
	msg := &api.Message{
		MessageID: 2,
		Chat:      *chat,
		From:      user,
		Text:      "попробуйте работает",
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: testChatTypeChannel},
		},
	}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if detector.calls != 0 {
		t.Fatalf("expected LLM detector not to be called, got %d calls", detector.calls)
	}
	if processSpamCalls != 0 {
		t.Fatalf("expected processSpam not to be called, got %d", processSpamCalls)
	}

	result := r.GetLastProcessingResult(msg.Chat.ID, msg.MessageID)
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != "User is already a member" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
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
		store:        &testNotSpammerStore{isNotSpammer: true},
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

	if service.insertedMember != 1 {
		t.Fatalf("expected member insertion, got %d", service.insertedMember)
	}
	if len(store.upserted) != 0 {
		t.Fatalf("expected no known non-member upsert, got %d", len(store.upserted))
	}
}

func TestHandleMessageExternalQuoteHeuristicFallbacks(t *testing.T) {
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

			service := &testBotService{}
			detector := &testSpamDetector{}
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

func TestDetectFirstMessageExternalQuoteHeuristic(t *testing.T) {
	t.Parallel()

	msg := &api.Message{
		Chat: api.Chat{ID: 100, Type: testChatTypeSupergroup},
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: testChatTypeChannel},
		},
		Quote:         &api.TextQuote{Text: "quote"},
		ForwardOrigin: &api.MessageOrigin{Type: api.MessageOriginChannel},
		ViaBot:        &api.User{ID: 55, IsBot: true},
	}

	result := detectFirstMessageExternalQuoteHeuristic(msg)
	if !result.Triggered {
		t.Fatal("expected heuristic to trigger")
	}
	if !result.HasExternalReply || !result.HasQuote || !result.HasForwardOrigin || !result.HasViaBot {
		t.Fatalf("unexpected heuristic flags: %#v", result)
	}
	if result.OriginType != api.MessageOriginChannel {
		t.Fatalf("unexpected origin type: %q", result.OriginType)
	}
	if result.OriginChatID != 999 {
		t.Fatalf("unexpected origin chat id: %d", result.OriginChatID)
	}
	if result.ViaBotID != 55 {
		t.Fatalf("unexpected via bot id: %d", result.ViaBotID)
	}
}
