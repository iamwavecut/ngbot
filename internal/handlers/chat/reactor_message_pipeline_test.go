package handlers

import (
	"context"
	"net/http"
	"strconv"
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
	botAPI         *api.BotAPI
	isMember       bool
	language       string
	insertedMember int
}

func (s *testBotService) GetBot() *api.BotAPI {
	if s.botAPI != nil {
		return s.botAPI
	}
	return &api.BotAPI{}
}

func (s *testBotService) GetDB() db.Client {
	return nil
}

func (s *testBotService) IsMember(context.Context, int64, int64) (bool, error) {
	return s.isMember, nil
}

func (s *testBotService) InsertMember(context.Context, int64, int64) error {
	s.insertedMember++
	return nil
}

func (s *testBotService) DeleteMember(context.Context, int64, int64) error {
	return nil
}

func (s *testBotService) GetSettings(context.Context, int64) (*db.Settings, error) {
	return nil, nil
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
	knownNonMember bool
	upserted       []db.ChatKnownNonMember
	deleted        [][2]int64
}

func (s *testReactorStore) ListChatSpamExamples(context.Context, int64, int, int) ([]*db.ChatSpamExample, error) {
	return nil, nil
}

func (s *testReactorStore) IsChatNotSpammer(context.Context, int64, int64, string) (bool, error) {
	return false, nil
}

func (s *testReactorStore) IsChatKnownNonMember(context.Context, int64, int64) (bool, error) {
	return s.knownNonMember, nil
}

func (s *testReactorStore) UpsertChatKnownNonMember(_ context.Context, record *db.ChatKnownNonMember) error {
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
	calls  int
	result *bool
}

func (d *testSpamDetector) IsSpam(context.Context, string, []string) (*bool, error) {
	d.calls++
	return d.result, nil
}

type testBanService struct {
	checkBanCalls int
	checkBan      bool
}

func (s *testBanService) Start(context.Context) error { return nil }
func (s *testBanService) Stop(context.Context) error  { return nil }
func (s *testBanService) CheckBan(context.Context, int64) (bool, error) {
	s.checkBanCalls++
	return s.checkBan, nil
}
func (s *testBanService) MuteUser(context.Context, int64, int64) error                { return nil }
func (s *testBanService) UnmuteUser(context.Context, int64, int64) error              { return nil }
func (s *testBanService) BanUserWithMessage(context.Context, int64, int64, int) error { return nil }
func (s *testBanService) UnbanUser(context.Context, int64, int64) error               { return nil }
func (s *testBanService) IsRestricted(context.Context, int64, int64) (bool, error)    { return false, nil }
func (s *testBanService) IsKnownBanned(int64) bool                                    { return false }

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

func TestSpamVoteCallbackUsesSpamCaseChatSettings(t *testing.T) {
	t.Parallel()

	var callbackAnswered bool
	var editSent bool
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case "answerCallbackQuery":
			callbackAnswered = true
			return true
		case "editMessageText":
			editSent = true
			return map[string]any{
				"message_id": 400,
				"date":       0,
				"chat": map[string]any{
					"id":   900,
					"type": "channel",
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

	service := botservice.NewService(ctx, botAPI, dbClient, log.NewEntry(log.New()))
	spamControl := moderation.NewSpamControl(service, config.SpamControl{
		MinVoters:            2,
		MaxVoters:            10,
		MinVotersPercentage:  0,
		VotingTimeoutMinutes: time.Minute,
	}, &testBanService{}, false)
	reactor := NewReactor(service, &testBanService{}, spamControl, nil, Config{})

	logChat := &api.Chat{ID: 900, Type: "channel"}
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
	if !callbackAnswered || !editSent {
		t.Fatalf("expected callback answer and edit, got answer=%v edit=%v", callbackAnswered, editSent)
	}

	votes, err := dbClient.GetSpamVotes(ctx, spamCase.ID)
	if err != nil {
		t.Fatalf("get spam votes: %v", err)
	}
	if len(votes) != 1 || votes[0].VoterID != voter.ID || votes[0].Vote {
		t.Fatalf("unexpected votes: %#v", votes)
	}
}

func TestReactionCountModeratesStoredMessageAuthor(t *testing.T) {
	t.Parallel()

	var bannedUserID string
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch method {
		case "deleteMessage":
			return true
		case "banChatMember":
			bannedUserID = r.Form.Get("user_id")
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	service := &testBotService{botAPI: botAPI}
	reactor := &Reactor{
		s:           service,
		store:       &testReactorStore{},
		config:      Config{FlaggedEmojis: []string{"👎"}},
		banService:  &testBanService{},
		lastResults: make(map[int64]*MessageProcessingResult),
		resultOrder: make([]int64, 0, maxLastResults),
	}

	chat := &api.Chat{ID: -100, Type: "supergroup"}
	author := &api.User{ID: 200, FirstName: "Author"}
	reactor.storeLastResult(400, &MessageProcessingResult{
		Message: &api.Message{
			MessageID: 400,
			Chat:      *chat,
			From:      author,
			Text:      "message",
		},
	})

	update := &api.Update{
		MessageReactionCount: &api.MessageReactionCountUpdated{
			Chat:      *chat,
			MessageID: 400,
			Reactions: []api.ReactionCount{{
				Type:       api.ReactionType{Type: "emoji", Emoji: "👎"},
				TotalCount: 5,
			}},
		},
	}

	proceed, err := reactor.Handle(context.Background(), update, chat, nil)
	if err != nil {
		t.Fatalf("handle reaction count: %v", err)
	}
	if !proceed {
		t.Fatal("expected handler to proceed")
	}
	if bannedUserID != "200" {
		t.Fatalf("expected stored message author to be banned, got user_id %q", bannedUserID)
	}
}

func TestCustomEmojiLookupFallsBackOnEmptyResponse(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if method != "getCustomEmojiStickers" {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return []any{}
	})
	reactor := &Reactor{s: &testBotService{botAPI: botAPI}}

	got := reactor.getEmojiFromReaction(api.ReactionType{Type: api.StickerTypeCustomEmoji, CustomEmoji: "custom-id"})
	if got != "" {
		t.Fatalf("expected empty fallback emoji, got %q", got)
	}
}

func TestHandleMessageExternalQuoteHeuristic(t *testing.T) {
	t.Parallel()

	service := &testBotService{language: "ru"}
	detector := &testSpamDetector{}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
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
		lastResults: make(map[int64]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: "supergroup"}
	user := &api.User{ID: 200}
	msg := &api.Message{
		MessageID: 1,
		Chat:      *chat,
		From:      user,
		Text:      "попробуйте работает",
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: "channel"},
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

	result := r.GetLastProcessingResult(int64(msg.MessageID))
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.IsSpam == nil || !*result.IsSpam {
		t.Fatalf("expected spam result, got %#v", result.IsSpam)
	}
	if result.SkipReason != "First-message external quote heuristic" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageCleanLeftUserRememberedAsKnownNonMember(t *testing.T) {
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
				"status": "left",
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
		lastResults: make(map[int64]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: "supergroup"}
	user := &api.User{ID: 200}
	msg := &api.Message{MessageID: 11, Chat: *chat, From: user, Text: "hello there"}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	if detector.calls != 1 {
		t.Fatalf("expected LLM detector to be called once, got %d", detector.calls)
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

func TestHandleMessageNotSpammerOverrideBypassesBanAndLLM(t *testing.T) {
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

	service := &testBotService{
		botAPI:   botAPI,
		language: "ru",
	}
	detector := &testSpamDetector{}
	banService := &testBanService{}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
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
		lastResults: make(map[int64]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: "supergroup"}
	user := &api.User{ID: 200, UserName: "override_user"}
	msg := &api.Message{
		MessageID: 10,
		Chat:      *chat,
		From:      user,
		Text:      "hello there",
	}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
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
	if service.insertedMember != 1 {
		t.Fatalf("expected member insertion, got %d", service.insertedMember)
	}

	result := r.GetLastProcessingResult(int64(msg.MessageID))
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

func TestHandleMessageKnownNonMemberBypassesFirstMessageChecks(t *testing.T) {
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
				"status": "left",
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
		lastResults: make(map[int64]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: "supergroup"}
	user := &api.User{ID: 200}
	msg := &api.Message{
		MessageID: 12,
		Chat:      *chat,
		From:      user,
		Text:      "reply from guest",
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: "channel"},
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
	if banService.checkBanCalls != 1 {
		t.Fatalf("expected ban check to run before bypass, got %d calls", banService.checkBanCalls)
	}

	result := r.GetLastProcessingResult(int64(msg.MessageID))
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != "User is a remembered non-member" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageExternalQuoteHeuristicDoesNotTriggerForNonFirstMessage(t *testing.T) {
	t.Parallel()

	service := &testBotService{isMember: true}
	detector := &testSpamDetector{}
	processSpamCalls := 0
	r := &Reactor{
		s:            service,
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
		lastResults: make(map[int64]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: "supergroup"}
	user := &api.User{ID: 200}
	msg := &api.Message{
		MessageID: 2,
		Chat:      *chat,
		From:      user,
		Text:      "попробуйте работает",
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: "channel"},
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

	result := r.GetLastProcessingResult(int64(msg.MessageID))
	if result == nil {
		t.Fatal("expected processing result")
	}
	if result.SkipReason != "User is already a member" {
		t.Fatalf("unexpected skip reason: %q", result.SkipReason)
	}
}

func TestHandleMessageCleanMemberInsertsMemberInsteadOfKnownNonMember(t *testing.T) {
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

	service := &testBotService{botAPI: botAPI}
	store := &testReactorStore{}
	detector := &testSpamDetector{result: boolPtr(false)}
	r := &Reactor{
		s:            service,
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
		lastResults: make(map[int64]*MessageProcessingResult),
	}

	chat := &api.Chat{ID: 100, Type: "supergroup"}
	user := &api.User{ID: 200}
	msg := &api.Message{MessageID: 13, Chat: *chat, From: user, Text: "hello there"}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	if err := r.handleMessage(context.Background(), msg, chat, user, settings); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
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
				lastResults: make(map[int64]*MessageProcessingResult),
			}

			chat := &api.Chat{ID: 100, Type: "supergroup"}
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
		Chat: api.Chat{ID: 100, Type: "supergroup"},
		ExternalReply: &api.ExternalReplyInfo{
			Origin: api.MessageOrigin{Type: api.MessageOriginChannel},
			Chat:   &api.Chat{ID: 999, Type: "channel"},
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
