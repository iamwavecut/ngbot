package handlers

import (
	"context"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

type testBotService struct {
	isMember bool
	language string
}

func (s *testBotService) GetBot() *api.BotAPI {
	return &api.BotAPI{}
}

func (s *testBotService) GetDB() db.Client {
	return nil
}

func (s *testBotService) IsMember(context.Context, int64, int64) (bool, error) {
	return s.isMember, nil
}

func (s *testBotService) InsertMember(context.Context, int64, int64) error {
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

type testReactorStore struct{}

func (s *testReactorStore) ListChatSpamExamples(context.Context, int64, int, int) ([]*db.ChatSpamExample, error) {
	return nil, nil
}

type testSpamDetector struct {
	calls int
}

func (d *testSpamDetector) IsSpam(context.Context, string, []string) (*bool, error) {
	d.calls++
	return nil, nil
}

type testBanService struct{}

func (s *testBanService) Start(context.Context) error                                 { return nil }
func (s *testBanService) Stop(context.Context) error                                  { return nil }
func (s *testBanService) CheckBan(context.Context, int64) (bool, error)               { return false, nil }
func (s *testBanService) MuteUser(context.Context, int64, int64) error                { return nil }
func (s *testBanService) UnmuteUser(context.Context, int64, int64) error              { return nil }
func (s *testBanService) BanUserWithMessage(context.Context, int64, int64, int) error { return nil }
func (s *testBanService) UnbanUser(context.Context, int64, int64) error               { return nil }
func (s *testBanService) IsRestricted(context.Context, int64, int64) (bool, error)    { return false, nil }
func (s *testBanService) IsKnownBanned(int64) bool                                    { return false }

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
