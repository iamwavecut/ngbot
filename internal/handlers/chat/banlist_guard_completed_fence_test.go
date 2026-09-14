package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

type completedFenceBanService struct {
	moderation.BanService
	available bool
}

func (*completedFenceBanService) IsKnownBanned(int64) bool { return true }

func (s *completedFenceBanService) ModerationAvailable(context.Context, int64) (bool, error) {
	return s.available, nil
}

func (s *completedFenceBanService) MarkModerationUnavailable(int64) { s.available = false }

func TestCompletedBanlistFenceReplayPreservesDeniedAndSuccessfulOutcomes(t *testing.T) {
	t.Parallel()
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful", true: "permission_denied"}[denied], func(t *testing.T) {
			dir := t.TempDir()
			client, err := sqlite.NewSQLiteClient(t.Context(), dir, "fence.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
			user := &api.User{ID: 200}
			if err := client.SetSettings(t.Context(), db.DefaultSettings(chat.ID)); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID}
			initialTrust, _, err := client.RecordSafeAuthorMessage(t.Context(), chat.ID, author, 1, now, 1, time.Hour, true)
			if err != nil {
				t.Fatal(err)
			}
			record := &db.MessageContext{ChatID: chat.ID, MessageID: 40, AuthorKind: author.Kind, AuthorID: author.ID, Text: "saved conversation", SentAt: now, UpdatedAt: now}
			if err := client.UpsertMessageContext(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			banCalls, deleteCalls := 0, 0
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				switch method {
				case testTelegramMethodBanChatMember:
					banCalls++
					if denied {
						return &testBotAPIError{code: 403, description: "not enough rights to restrict chat member"}
					}
					return true
				case testTelegramMethodDeleteMessage:
					deleteCalls++
					return true
				default:
					t.Fatalf("unexpected Telegram method: %s", method)
					return nil
				}
			})
			service := &completedFenceBanService{BanService: moderation.NewBanService(botAPI, client), available: true}
			guard := NewBanlistGuard(botAPI, client, service)
			message := &api.Message{MessageID: 40, Chat: *chat, From: user, Text: record.Text}
			update := &api.Update{UpdateID: 900, Message: message}
			if proceed, err := guard.Handle(t.Context(), update, chat, user); err != nil || proceed {
				t.Fatalf("initial terminal action: proceed=%v err=%v", proceed, err)
			}
			fence, err := client.BeginModerationAction(t.Context(), &db.ModerationActionFence{ActionKey: "banlist:900:-100:200:40", ChatID: chat.ID, UserID: user.ID, MessageID: 40, BanUntil: now.Add(time.Hour)}, "inspect", now)
			if err != nil || fence == nil || fence.Status != db.ModerationActionCompleted || (fence.LastError == "permission denied") != denied {
				t.Fatalf("terminal outcome was not persisted: fence=%+v err=%v", fence, err)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			client, err = sqlite.NewSQLiteClient(t.Context(), dir, "fence.db")
			if err != nil {
				t.Fatal(err)
			}
			service = &completedFenceBanService{BanService: moderation.NewBanService(botAPI, client), available: true}
			guard = NewBanlistGuard(botAPI, client, service)
			outcome := guard.enforceDurableBanlistedMessage(t.Context(), client, update.UpdateID, message, chat, user)
			if outcome.err != nil || outcome.moderationAvailable == denied || outcome.userBanned == denied || outcome.messageDeleted == denied {
				t.Errorf("persisted replay claimed wrong effects after rights recovery: outcome=%+v denied=%v", outcome, denied)
			}
			if proceed, err := guard.Handle(t.Context(), update, chat, user); err != nil || proceed {
				t.Fatalf("replayed terminal action: proceed=%v err=%v", proceed, err)
			}
			if banCalls != 1 || deleteCalls != map[bool]int{false: 1, true: 0}[denied] {
				t.Fatalf("completed replay repeated Telegram effects: bans=%d deletes=%d", banCalls, deleteCalls)
			}
			record.UpdatedAt = now.Add(time.Second)
			if err := client.UpsertMessageContext(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			if saved, err := client.MessageContext(t.Context(), chat.ID, 40); err != nil || (saved != nil) != denied {
				t.Fatalf("terminal replay corrupted context/tombstone: saved=%+v denied=%v err=%v", saved, denied, err)
			}
			trust, err := client.MessageTrust(t.Context(), chat.ID, author)
			if err != nil || trust == nil {
				t.Fatalf("load trust: trust=%+v err=%v", trust, err)
			}
			if denied && (trust.SafeMessages != initialTrust.SafeMessages || trust.TrustedUntil != initialTrust.TrustedUntil) {
				t.Fatalf("denied replay reset trust: before=%+v after=%+v", initialTrust, trust)
			}
			if !denied && (trust.SafeMessages != 0 || trust.TrustedUntil.Valid) {
				t.Fatalf("successful replay lost trust reset: %+v", trust)
			}
		})
	}
}
