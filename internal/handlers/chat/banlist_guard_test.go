package handlers

import (
	"context"
	"errors"
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
	guard := NewBanlistGuard(botAPI, &testNotSpammerStore{}, banService)
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

func TestBanlistGuardStopsEditedMessageBeforeDownstreamHandlers(t *testing.T) {
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
	guard := NewBanlistGuard(botAPI, &testNotSpammerStore{}, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	message := &api.Message{MessageID: 43, Chat: *chat, From: user, Text: "edited spam"}

	proceed, err := guard.Handle(context.Background(), &api.Update{EditedMessage: message}, chat, user)
	if err != nil {
		t.Fatalf("handle edited banlisted message: %v", err)
	}
	if proceed {
		t.Fatal("expected terminal banlist guard to stop edited message")
	}
	if len(banService.bans) != 1 || banService.bans[0].messageID != message.MessageID {
		t.Fatalf("unexpected direct bans: %#v", banService.bans)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected edited message deletion, got %d calls", deleteCalls)
	}
}

func TestBanlistGuardNoRightsStopsWithoutTelegramRetry(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("unexpected bot method in no-rights mode: %s", method)
		return nil
	})
	banService := &testBanService{knownBanned: true, moderationUnavailable: true}
	guard := NewBanlistGuard(botAPI, &testNotSpammerStore{}, banService)
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
	guard := NewBanlistGuard(&api.BotAPI{}, &testNotSpammerStore{}, banService)
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

func TestBanlistGuardAllowsManuallyAllowlistedUser(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("unexpected bot method for manually allowlisted user: %s", method)
		return nil
	})
	banService := &testBanService{knownBanned: true}
	guard := NewBanlistGuard(botAPI, &testNotSpammerStore{isNotSpammer: true}, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, UserName: "allowlisted"}
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: "/settings"}

	proceed, err := guard.Handle(context.Background(), &api.Update{Message: message}, chat, user)
	if err != nil {
		t.Fatalf("handle manually allowlisted command: %v", err)
	}
	if !proceed {
		t.Fatal("expected manually allowlisted user to reach downstream handlers")
	}
	if len(banService.bans) != 0 {
		t.Fatalf("manually allowlisted user was incorrectly banned: %#v", banService.bans)
	}
}

func TestBanlistGuardAllowlistLookupFailureContinuesBan(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodDeleteMessage {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return true
	})
	banService := &testBanService{knownBanned: true}
	store := &testNotSpammerStore{notSpammerErr: errors.New("database unavailable")}
	guard := NewBanlistGuard(botAPI, store, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, UserName: "candidate"}
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: "message"}

	proceed, err := guard.Handle(context.Background(), &api.Update{Message: message}, chat, user)
	if err != nil {
		t.Fatalf("handle allowlist lookup failure: %v", err)
	}
	if proceed {
		t.Fatal("expected allowlist lookup failure not to grant an exemption")
	}
	if len(banService.bans) != 1 {
		t.Fatalf("expected banlist enforcement after lookup failure, got %#v", banService.bans)
	}
}
