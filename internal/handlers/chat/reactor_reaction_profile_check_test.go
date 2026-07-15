package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

func TestHandleMessageReactionModeratesUnknownUserProfileSpam(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: testFirstNameBad, UserName: testBadUsername}
	detector := &testSpamDetector{result: boolPtr(true)}

	var deletedAllUserID string
	var bannedUserID string
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(testMemberStatusLeft, false, false, false)
		case testTelegramMethodGetChat:
			if got := r.Form.Get("chat_id"); got != "777" {
				t.Fatalf("expected user profile chat lookup, got chat_id %q", got)
			}
			return map[string]any{
				"id":              777,
				testJSONType:      telegramChatTypePrivate,
				testJSONFirstName: testFirstNameBad,
				logFieldUsername:  testBadUsername,
				"bio":             "Удаленная работа от 500 долларов в день, подробности в личку",
				"personal_chat": map[string]any{
					"id":             -100999,
					testJSONType:     testChatTypeChannel,
					testJSONTitle:    "Fast income",
					logFieldUsername: "fast_income_bot",
				},
			}
		case "getUserPersonalChatMessages":
			if got := r.Form.Get("user_id"); got != "777" {
				t.Fatalf("expected personal chat messages for user 777, got %q", got)
			}
			return []map[string]any{{
				logFieldMessageID: 1,
				testJSONDate:      1,
				logFieldChat: map[string]any{
					"id":          -100999,
					testJSONType:  testChatTypeChannel,
					testJSONTitle: "Fast income",
				},
				"text": "Казино бот с бонусом, переходи по ссылке",
			}}
		case testTelegramMethodDeleteAllReactions:
			deletedAllUserID = r.Form.Get("user_id")
			return true
		case testTelegramMethodBanChatMember:
			bannedUserID = r.Form.Get("user_id")
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
		}
		return nil
	})

	reactor := &Reactor{
		s:            &testBotService{botAPI: botAPI, settings: settings},
		bot:          botAPI,
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "❤",
			}},
		},
	}

	proceed, err := reactor.Handle(context.Background(), update, chat, user)
	if err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if !proceed {
		t.Fatal("expected handler to proceed")
	}
	if detector.calls != 1 {
		t.Fatalf("expected one profile spam check, got %d", detector.calls)
	}
	if len(detector.messages) != 1 || !strings.Contains(detector.messages[0], "Удаленная работа") || !strings.Contains(detector.messages[0], "Казино бот") {
		t.Fatalf("expected enriched profile text, got %#v", detector.messages)
	}
	if deletedAllUserID != "777" {
		t.Fatalf("expected all reactions from user 777 to be deleted, got %q", deletedAllUserID)
	}
	if bannedUserID != "777" {
		t.Fatalf("expected user 777 to be banned, got %q", bannedUserID)
	}
}

func TestHandleMessageReactionRemembersUnknownUserProfileNotSpam(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: "Clean", UserName: "cleanworker"}
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(testMemberStatusLeft, false, false, false)
		case testTelegramMethodGetChat:
			return map[string]any{
				"id":              777,
				testJSONType:      telegramChatTypePrivate,
				testJSONFirstName: "Clean",
				logFieldUsername:  "cleanworker",
				"bio":             "Local neighbor and regular reader",
			}
		case "getUserPersonalChatMessages":
			return []any{}
		default:
			t.Fatalf("unexpected bot method: %s", method)
		}
		return nil
	})

	reactor := &Reactor{
		s:            &testBotService{botAPI: botAPI, settings: settings},
		bot:          botAPI,
		store:        store,
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👍",
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, user); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 1 {
		t.Fatalf("expected one profile spam check, got %d", detector.calls)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected clean non-member to be remembered once, got %#v", store.upserted)
	}
	if store.upserted[0].ChatID != chat.ID || store.upserted[0].UserID != user.ID {
		t.Fatalf("unexpected known non-member record: %#v", store.upserted[0])
	}
}

func TestHandleMessageReactionSkipsKnownMemberAfterTelegramMembershipCheck(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: "Member", UserName: telegramMemberStatus}
	detector := &testSpamDetector{result: boolPtr(true)}
	service := &testBotService{settings: settings}

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(telegramMemberStatus, false, false, false)
		default:
			t.Fatalf("unexpected bot method: %s", method)
		}
		return nil
	})
	service.botAPI = botAPI

	reactor := &Reactor{
		s:            service,
		bot:          service.GetBot(),
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👍",
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, user); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 0 {
		t.Fatalf("expected known member to bypass profile spam check, got %d calls", detector.calls)
	}
	if service.insertedMember != 1 {
		t.Fatalf("expected member to be remembered after Telegram check, got %d inserts", service.insertedMember)
	}
}

func TestHandleMessageReactionSkipsRememberedNonMember(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: "Known", UserName: "knownreader"}
	detector := &testSpamDetector{result: boolPtr(true)}

	reactor := &Reactor{
		s:            &testBotService{settings: settings},
		store:        &testReactorStore{knownNonMember: true},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👍",
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, user); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 0 {
		t.Fatalf("expected remembered non-member to bypass profile spam check, got %d calls", detector.calls)
	}
}

func TestHandleMessageReactionModeratesActorChatProfileSpam(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	actorChat := &api.Chat{ID: -100777, Type: testChatTypeChannel, Title: testSpamChannelTitle, UserName: "spam_channel"}
	detector := &testSpamDetector{result: boolPtr(true)}

	var deletedAllActorChatID string
	var bannedSenderChatID string
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChat:
			switch got := r.Form.Get("chat_id"); got {
			case "-100123":
				return map[string]any{
					"id":          -100123,
					testJSONType:  testChatTypeSupergroup,
					testJSONTitle: testGroupTitle,
				}
			case "-100777":
			default:
				t.Fatalf("unexpected chat lookup: %q", got)
			}
			return map[string]any{
				"id":                -100777,
				testJSONType:        testChatTypeChannel,
				testJSONTitle:       testSpamChannelTitle,
				logFieldUsername:    "spam_channel",
				testJSONDescription: "Крипто-казино, бонусы, быстрый заработок",
				"pinned_message": map[string]any{
					logFieldMessageID: 10,
					testJSONDate:      1,
					logFieldChat: map[string]any{
						"id":          -100777,
						testJSONType:  testChatTypeChannel,
						testJSONTitle: testSpamChannelTitle,
					},
					"text": "Переходи в бота и забирай бонус",
				},
			}
		case testTelegramMethodDeleteAllReactions:
			deletedAllActorChatID = r.Form.Get("actor_chat_id")
			return true
		case testTelegramMethodBanChatSenderChat:
			bannedSenderChatID = r.Form.Get("sender_chat_id")
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
		}
		return nil
	})

	reactor := &Reactor{
		s:            &testBotService{botAPI: botAPI, settings: settings},
		bot:          botAPI,
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			ActorChat: actorChat,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👍",
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, nil); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 1 {
		t.Fatalf("expected one actor chat spam check, got %d", detector.calls)
	}
	if len(detector.messages) != 1 || !strings.Contains(detector.messages[0], "Крипто-казино") || !strings.Contains(detector.messages[0], "забирай бонус") {
		t.Fatalf("expected enriched actor chat text, got %#v", detector.messages)
	}
	if deletedAllActorChatID != "-100777" {
		t.Fatalf("expected all reactions from actor chat to be deleted, got %q", deletedAllActorChatID)
	}
	if bannedSenderChatID != "-100777" {
		t.Fatalf("expected sender chat to be banned, got %q", bannedSenderChatID)
	}
}

func TestHandleMessageReactionSkipsWhenReactionProfileCheckDisabled(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	settings.ReactionProfileCheckEnabled = false
	user := &api.User{ID: 777, FirstName: testFirstNameBad, UserName: testBadUsername}
	detector := &testSpamDetector{result: boolPtr(true)}

	reactor := &Reactor{
		s:            &testBotService{settings: settings},
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👍",
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, user); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 0 {
		t.Fatalf("expected disabled reaction profile check to bypass detector, got %d calls", detector.calls)
	}
}

func TestHandleMessageReactionSkipsReactionRemoval(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: testChatTypeSupergroup, Title: testGroupTitle}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: testFirstNameBad, UserName: testBadUsername}
	detector := &testSpamDetector{result: boolPtr(true)}

	reactor := &Reactor{
		s:            &testBotService{settings: settings},
		store:        &testReactorStore{},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:  make([]messageResultKey, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			OldReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👍",
			}},
			NewReaction: nil,
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, nil); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 0 {
		t.Fatalf("expected reaction removal to bypass detector, got %d calls", detector.calls)
	}
}

func TestReactionPrivilegeFailureDisablesModeration(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodDeleteAllReactions:
			return true
		case testTelegramMethodBanChatMember:
			return &testBotAPIError{code: http.StatusBadRequest, description: "Bad Request: CHAT_ADMIN_REQUIRED"}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	banService := &testBanService{}
	reactor := &Reactor{bot: botAPI, banService: banService}

	err := reactor.punishReactionUser(context.Background(), -100, 40, 200, reactor.getLogEntry())
	if err == nil {
		t.Fatal("expected privilege failure")
	}
	if !banService.moderationUnavailable || !banService.markedUnavailable {
		t.Fatalf("reaction privilege failure did not disable moderation: %#v", banService)
	}
}

var _ moderation.BanService = (*testBanService)(nil)
