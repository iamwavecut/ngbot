package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
)

type testModerationFenceStore struct {
	testNotSpammerStore
	action          *db.ModerationActionFence
	failBanPersist  bool
	reconciliations int
}

func (s *testModerationFenceStore) BeginModerationAction(_ context.Context, action *db.ModerationActionFence, owner string, now time.Time) (*db.ModerationActionFence, error) {
	if s.action == nil {
		copy := *action
		copy.Status = db.ModerationActionStarted
		copy.Owner = owner
		copy.CreatedAt = now
		copy.UpdatedAt = now
		s.action = &copy
	}
	copy := *s.action
	return &copy, nil
}

func (s *testModerationFenceStore) AdvanceModerationAction(_ context.Context, _ string, owner, expectedStatus, nextStatus, lastError string, now time.Time) (bool, error) {
	if s.failBanPersist && expectedStatus == db.ModerationActionStarted && nextStatus == db.ModerationActionBanned {
		s.failBanPersist = false
		return false, errors.New("simulated crash before ban persistence")
	}
	if s.action.Owner != owner || s.action.Status != expectedStatus {
		return false, nil
	}
	s.action.Status = nextStatus
	s.action.LastError = lastError
	s.action.UpdatedAt = now
	if nextStatus == db.ModerationActionReconciliation {
		s.reconciliations++
	}
	return true, nil
}

func (s *testModerationFenceStore) MarkModerationActionEffectStarted(_ context.Context, _ string, owner string, now time.Time) (bool, error) {
	if s.action.Owner != owner || s.action.Status != db.ModerationActionStarted || s.action.EffectStartedAt.Valid {
		return false, nil
	}
	s.action.EffectStartedAt.Valid = true
	s.action.EffectStartedAt.Time = now
	return true, nil
}

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
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: testSpamMessageText}

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

func TestBanlistGuardCapabilityUnknownReturnsRetryableFailure(t *testing.T) {
	t.Parallel()

	banService := &testBanService{knownBanned: true, moderationErr: errors.New("telegram unavailable")}
	guard := NewBanlistGuard(&api.BotAPI{}, &testNotSpammerStore{}, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: testSpamMessageText}

	proceed, err := guard.Handle(t.Context(), &api.Update{Message: message}, chat, user)
	if proceed {
		t.Fatal("capability-unknown banlist update reached downstream handlers")
	}
	failure := bot.ClassifyUpdateFailure(err)
	if failure.Source != bot.UpdateFailureCapability || failure.Disposition != bot.UpdateFailureRetryable {
		t.Fatalf("capability failure = %#v", failure)
	}
}

func TestBanlistGuardLeavesJoinServiceMessageForGatekeeper(t *testing.T) {
	t.Parallel()

	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 200}
	joined := api.User{ID: 300}
	banService := &testBanService{knownBannedUsers: map[int64]bool{actor.ID: true}}
	guard := NewBanlistGuard(&api.BotAPI{}, &testNotSpammerStore{}, banService)
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

func TestBanlistGuardRemovesBannedJoinBeforeGatekeeper(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodDeleteMessage {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return true
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 200}
	joined := api.User{ID: 300}
	banService := &testBanService{knownBannedUsers: map[int64]bool{joined.ID: true}}
	guard := NewBanlistGuard(botAPI, &testNotSpammerStore{}, banService)
	message := &api.Message{MessageID: 42, Chat: *chat, From: actor, NewChatMembers: []api.User{joined}}

	proceed, err := guard.Handle(t.Context(), &api.Update{Message: message}, chat, actor)
	if err != nil {
		t.Fatalf("handle banned join: %v", err)
	}
	if proceed {
		t.Fatal("banned joined user reached gatekeeper")
	}
	if len(message.NewChatMembers) != 0 {
		t.Fatalf("banned joiners left in update: %#v", message.NewChatMembers)
	}
	if len(banService.bans) != 1 || banService.bans[0].userID != joined.ID {
		t.Fatalf("join bans = %#v", banService.bans)
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

func TestBanlistGuardDoesNotRepeatAmbiguousBanAfterCrash(t *testing.T) {
	t.Parallel()

	deleteCalls := 0
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodDeleteMessage {
			deleteCalls++
			return true
		}
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := &testModerationFenceStore{failBanPersist: true}
	banService := &testBanService{knownBanned: true}
	guard := NewBanlistGuard(botAPI, store, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200}
	message := &api.Message{MessageID: 42, Chat: *chat, From: user, Text: testSpamMessageText}
	update := &api.Update{UpdateID: 900, Message: message}

	if _, err := guard.Handle(t.Context(), update, chat, user); err == nil {
		t.Fatal("expected persistence failure after Telegram ban")
	}
	_, err := guard.Handle(t.Context(), update, chat, user)
	failure := bot.ClassifyUpdateFailure(err)
	if failure.Disposition != bot.UpdateFailureTerminal || failure.Reason != "moderation_effect_ambiguous" {
		t.Fatalf("replay failure = %#v", failure)
	}
	if len(banService.bans) != 1 {
		t.Fatalf("Telegram ban calls = %d, want 1", len(banService.bans))
	}
	if deleteCalls != 0 || store.reconciliations != 1 {
		t.Fatalf("delete calls=%d reconciliations=%d", deleteCalls, store.reconciliations)
	}
	if len(banService.banDeadlines) != 1 || !banService.banDeadlines[0].Equal(store.action.BanUntil) {
		t.Fatalf("stable ban deadlines = %#v, action=%#v", banService.banDeadlines, store.action)
	}
}

func TestBanlistGuardChecksProviderBeforeJoinRequestFeatureRouting(t *testing.T) {
	t.Parallel()

	banService := &testBanService{checkBan: true}
	guard := NewBanlistGuard(&api.BotAPI{}, &testNotSpammerStore{}, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, UserName: "provider_banned"}
	update := &api.Update{ChatJoinRequest: &api.ChatJoinRequest{Chat: *chat, From: *user}}

	proceed, err := guard.Handle(t.Context(), update, chat, user)
	if err != nil {
		t.Fatalf("handle provider-banned join request: %v", err)
	}
	if proceed {
		t.Fatal("provider-banned join request reached feature routing")
	}
	if len(banService.bans) != 1 || banService.bans[0].userID != user.ID {
		t.Fatalf("join request bans = %#v", banService.bans)
	}
}

func TestBanlistGuardModeratesMemberUpdateSubjectInsteadOfAdministratorActor(t *testing.T) {
	t.Parallel()

	banService := &testBanService{checkBan: true}
	guard := NewBanlistGuard(&api.BotAPI{}, &testNotSpammerStore{}, banService)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 200, UserName: "admin"}
	subject := &api.User{ID: 300, UserName: "joined_user"}
	update := &api.Update{ChatMember: &api.ChatMemberUpdated{
		Chat:          *chat,
		From:          *actor,
		NewChatMember: api.ChatMember{User: subject, Status: telegramMemberStatus},
	}}

	proceed, err := guard.Handle(t.Context(), update, chat, actor)
	if err != nil {
		t.Fatalf("handle member update: %v", err)
	}
	if proceed {
		t.Fatal("provider-banned member update reached feature routing")
	}
	if len(banService.bans) != 1 || banService.bans[0].userID != subject.ID {
		t.Fatalf("member update bans = %#v", banService.bans)
	}
}
