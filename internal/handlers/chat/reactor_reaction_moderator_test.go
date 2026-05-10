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

	chat := &api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club"}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: "Bad", UserName: "badworker"}
	detector := &testSpamDetector{result: boolPtr(true)}

	var deletedAllUserID string
	var bannedUserID string
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse("left", false, false, false)
		case testTelegramMethodGetChat:
			if got := r.Form.Get("chat_id"); got != "777" {
				t.Fatalf("expected user profile chat lookup, got chat_id %q", got)
			}
			return map[string]any{
				"id":         777,
				"type":       "private",
				"first_name": "Bad",
				"username":   "badworker",
				"bio":        "Удаленная работа от 500 долларов в день, подробности в личку",
				"personal_chat": map[string]any{
					"id":       -100999,
					"type":     "channel",
					"title":    "Fast income",
					"username": "fast_income_bot",
				},
			}
		case "getUserPersonalChatMessages":
			if got := r.Form.Get("user_id"); got != "777" {
				t.Fatalf("expected personal chat messages for user 777, got %q", got)
			}
			return []map[string]any{{
				"message_id": 1,
				"date":       1,
				"chat": map[string]any{
					"id":    -100999,
					"type":  "channel",
					"title": "Fast income",
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
		store:        &testReactorStore{},
		config:       Config{FlaggedEmojis: []string{"👎"}},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[int64]*MessageProcessingResult),
		resultOrder:  make([]int64, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👎",
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

func TestHandleMessageReactionSkipsKnownMemberAfterTelegramMembershipCheck(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club"}
	settings := db.DefaultSettings(chat.ID)
	user := &api.User{ID: 777, FirstName: "Member", UserName: "member"}
	detector := &testSpamDetector{result: boolPtr(true)}
	service := &testBotService{settings: settings}

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse("member", false, false, false)
		default:
			t.Fatalf("unexpected bot method: %s", method)
		}
		return nil
	})
	service.botAPI = botAPI

	reactor := &Reactor{
		s:            service,
		store:        &testReactorStore{},
		config:       Config{FlaggedEmojis: []string{"👎"}},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[int64]*MessageProcessingResult),
		resultOrder:  make([]int64, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👎",
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

func TestHandleMessageReactionModeratesActorChatProfileSpam(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club"}
	settings := db.DefaultSettings(chat.ID)
	actorChat := &api.Chat{ID: -100777, Type: "channel", Title: "Spam Channel", UserName: "spam_channel"}
	detector := &testSpamDetector{result: boolPtr(true)}

	var deletedAllActorChatID string
	var bannedSenderChatID string
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}

		switch method {
		case testTelegramMethodGetChat:
			if got := r.Form.Get("chat_id"); got != "-100777" {
				t.Fatalf("expected actor chat lookup, got chat_id %q", got)
			}
			return map[string]any{
				"id":          -100777,
				"type":        "channel",
				"title":       "Spam Channel",
				"username":    "spam_channel",
				"description": "Крипто-казино, бонусы, быстрый заработок",
				"pinned_message": map[string]any{
					"message_id": 10,
					"date":       1,
					"chat": map[string]any{
						"id":    -100777,
						"type":  "channel",
						"title": "Spam Channel",
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
		store:        &testReactorStore{},
		config:       Config{FlaggedEmojis: []string{"👎"}},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[int64]*MessageProcessingResult),
		resultOrder:  make([]int64, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			ActorChat: actorChat,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👎",
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

func TestHandleMessageReactionSkipsWhenReactionModerationDisabled(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club"}
	settings := db.DefaultSettings(chat.ID)
	settings.ReactionModerationEnabled = false
	user := &api.User{ID: 777, FirstName: "Bad", UserName: "badworker"}
	detector := &testSpamDetector{result: boolPtr(true)}

	reactor := &Reactor{
		s:            &testBotService{settings: settings},
		store:        &testReactorStore{},
		config:       Config{FlaggedEmojis: []string{"👎"}},
		spamDetector: detector,
		banService:   &testBanService{},
		lastResults:  make(map[int64]*MessageProcessingResult),
		resultOrder:  make([]int64, 0, maxLastResults),
	}

	update := &api.Update{
		MessageReaction: &api.MessageReactionUpdated{
			Chat:      *chat,
			MessageID: 42,
			User:      user,
			NewReaction: []api.ReactionType{{
				Type:  api.ReactionTypeEmoji,
				Emoji: "👎",
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, user); err != nil {
		t.Fatalf("handle reaction: %v", err)
	}
	if detector.calls != 0 {
		t.Fatalf("expected disabled reaction moderation to bypass detector, got %d calls", detector.calls)
	}
}

func TestHandleReactionCountDoesNotBanOriginalMessageAuthor(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100123, Type: "supergroup", Title: "Wave Club"}
	settings := db.DefaultSettings(chat.ID)

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		t.Fatalf("unexpected bot method for reaction count: %s", method)
		return nil
	})

	reactor := &Reactor{
		s:           &testBotService{botAPI: botAPI, settings: settings},
		store:       &testReactorStore{},
		config:      Config{FlaggedEmojis: []string{"👎"}},
		banService:  &testBanService{},
		lastResults: make(map[int64]*MessageProcessingResult),
		resultOrder: make([]int64, 0, maxLastResults),
	}
	reactor.storeLastResult(400, &MessageProcessingResult{
		Message: &api.Message{
			MessageID: 400,
			Chat:      *chat,
			From:      &api.User{ID: 200, FirstName: "Author"},
			Text:      "message",
		},
	})

	update := &api.Update{
		MessageReactionCount: &api.MessageReactionCountUpdated{
			Chat:      *chat,
			MessageID: 400,
			Reactions: []api.ReactionCount{{
				Type:       api.ReactionType{Type: api.ReactionTypeEmoji, Emoji: "👎"},
				TotalCount: 5,
			}},
		},
	}

	if _, err := reactor.Handle(context.Background(), update, chat, nil); err != nil {
		t.Fatalf("handle reaction count: %v", err)
	}
}

var _ moderation.BanService = (*testBanService)(nil)
