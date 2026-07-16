package handlers

import (
	"context"
	"net/http"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestBanlistGuardStopsCommandBeforeDownstreamHandlers(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodDeleteMessage {
			t.Fatalf("unexpected bot method: %s", method)
		}
		deleteCalls++
		return true
	})
	banService := &testBanService{knownBanned: true}
	guard := NewBanlistGuard(botAPI, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: "/settings"}

	proceed, err := guard.Handle(context.Background(), &api.Update{Message: message}, chat, user)
	if err != nil {
		t.Fatalf("handle banlisted command: %v", err)
	}
	if proceed {
		t.Fatal("expected terminal banlist guard to stop downstream handlers")
	}
	if len(banService.bans) != 1 || banService.bans[0].messageID != message.MessageID {
		t.Fatalf("unexpected direct bans: %#v", banService.bans)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected command message deletion, got %d calls", deleteCalls)
	}
}

func TestBanlistGuardNoRightsStopsWithoutTelegramRetry(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("unexpected bot method in no-rights mode: %s", method)
		return nil
	})
	banService := &testBanService{knownBanned: true, moderationUnavailable: true}
	guard := NewBanlistGuard(botAPI, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: "spam"}

	proceed, err := guard.Handle(context.Background(), &api.Update{Message: message}, chat, user)
	if err != nil {
		t.Fatalf("handle banlisted no-rights message: %v", err)
	}
	if proceed {
		t.Fatal("expected known banlisted message to stay terminal in no-rights mode")
	}
	if len(banService.bans) != 0 {
		t.Fatalf("expected no Telegram ban retry, got %#v", banService.bans)
	}
}

func TestBanlistGuardLeavesJoinServiceMessageForGatekeeper(t *testing.T) {
	t.Parallel()

	banService := &testBanService{knownBanned: true}
	guard := NewBanlistGuard(&api.BotAPI{}, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 200}
	joined := api.User{ID: 300}
	message := &api.Message{MessageID: 42, Chat: *chat, From: actor, NewChatMembers: []api.User{joined}}

	proceed, err := guard.Handle(context.Background(), &api.Update{Message: message}, chat, actor)
	if err != nil {
		t.Fatalf("handle join service message: %v", err)
	}
	if !proceed {
		t.Fatal("expected join service message to reach gatekeeper")
	}
	if len(banService.bans) != 0 {
		t.Fatalf("join actor was incorrectly banned: %#v", banService.bans)
	}
}
