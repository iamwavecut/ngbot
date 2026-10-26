package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"

	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

const (
	globalIndulgenceTestOwnerID int64 = 7
	globalIndulgenceSeedEntries       = 2
	globalIndulgenceAddUserID         = "/global_indulgence add 12345"
)

type forbiddenBanService struct {
	moderation.BanService
	t *testing.T
}

func (s forbiddenBanService) UnbanUser(context.Context, int64, int64) error {
	s.t.Error("global indulgence must not unban users")
	return nil
}

func (s forbiddenBanService) UnmuteUser(context.Context, int64, int64) error {
	s.t.Error("global indulgence must not unmute users")
	return nil
}

type globalIndulgenceSeed struct {
	globalUserID   int64
	globalUsername int64
	perChat        int64
}

func TestGlobalIndulgenceCommand(t *testing.T) {
	i18n.Init()

	cases := []struct {
		name        string
		ownerID     int64
		senderID    int64
		chatID      int64
		chatType    string
		emptyGlobal bool
		text        func(seed globalIndulgenceSeed) string
		wantProceed bool
		wantReply   func(seed globalIndulgenceSeed) []string
		rejectReply []string
		check       func(t *testing.T, store adminTestDB, seed globalIndulgenceSeed)
	}{
		{
			name: "owner lists global entries",
			text: func(globalIndulgenceSeed) string { return "/global_indulgence" },
			wantReply: func(seed globalIndulgenceSeed) []string {
				return []string{
					"Global indulgence",
					"#" + strconv.FormatInt(seed.globalUserID, 10) + " 777001",
					"#" + strconv.FormatInt(seed.globalUsername, 10) + " @spam_fighter",
				}
			},
			rejectReply: []string{"777002"},
			check:       requireSeedGlobalEntriesKept,
		},
		{
			name:        "owner adds user ID",
			emptyGlobal: true,
			text:        func(globalIndulgenceSeed) string { return globalIndulgenceAddUserID },
			wantReply:   staticReply("Global indulgence entry added"),
			check: func(t *testing.T, store adminTestDB, seed globalIndulgenceSeed) {
				t.Helper()
				requireGlobalEntry(t, store, db.NotSpammerMatchTypeUserID, "12345")
			},
		},
		{
			name:        "owner adds username",
			emptyGlobal: true,
			text:        func(globalIndulgenceSeed) string { return "/global_indulgence add @Some_User" },
			wantReply:   staticReply("Global indulgence entry added"),
			check: func(t *testing.T, store adminTestDB, seed globalIndulgenceSeed) {
				t.Helper()
				requireGlobalEntry(t, store, db.NotSpammerMatchTypeUsername, "some_user")
			},
		},
		{
			name: "owner deletes global entry",
			text: func(seed globalIndulgenceSeed) string {
				return "/global_indulgence del " + strconv.FormatInt(seed.globalUserID, 10)
			},
			wantReply: staticReply("Global indulgence entry deleted"),
			check: func(t *testing.T, store adminTestDB, seed globalIndulgenceSeed) {
				t.Helper()
				if override, err := store.GetChatNotSpammerOverride(t.Context(), 0, seed.globalUserID); err != nil || override != nil {
					t.Fatalf("deleted entry = %+v, %v", override, err)
				}
			},
		},
		{
			name: "owner cannot delete per-chat entry",
			text: func(seed globalIndulgenceSeed) string {
				return "/global_indulgence del " + strconv.FormatInt(seed.perChat, 10)
			},
			wantReply: staticReply("Invalid input"),
			check: func(t *testing.T, store adminTestDB, seed globalIndulgenceSeed) {
				t.Helper()
				if override, err := store.GetChatNotSpammerOverride(t.Context(), -100, seed.perChat); err != nil || override == nil {
					t.Fatalf("per-chat entry = %+v, %v; want kept", override, err)
				}
			},
		},
		{
			name:      "owner adds invalid reference",
			text:      func(globalIndulgenceSeed) string { return "/global_indulgence add !!!" },
			wantReply: staticReply("Invalid input"),
			check:     requireSeedGlobalEntriesKept,
		},
		{
			name:      "owner gets usage for unknown action",
			text:      func(globalIndulgenceSeed) string { return "/global_indulgence purge" },
			wantReply: staticReply("Usage: /global_indulgence add"),
			check:     requireSeedGlobalEntriesKept,
		},
		{
			name:        "non-owner is ignored",
			senderID:    8,
			text:        func(globalIndulgenceSeed) string { return globalIndulgenceAddUserID },
			wantProceed: true,
			check:       requireSeedGlobalEntriesKept,
		},
		{
			name:        "owner in group is ignored",
			chatID:      -100,
			chatType:    "supergroup",
			text:        func(globalIndulgenceSeed) string { return globalIndulgenceAddUserID },
			wantProceed: true,
			check:       requireSeedGlobalEntriesKept,
		},
		{
			name:        "disabled owner ignores everyone",
			ownerID:     -1,
			text:        func(globalIndulgenceSeed) string { return globalIndulgenceAddUserID },
			wantProceed: true,
			check:       requireSeedGlobalEntriesKept,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := newAdminTestDB(t, ctx)
			seed := seedGlobalIndulgence(t, store, tc.emptyGlobal)

			var mu sync.Mutex
			var replies []string
			botAPI := newAdminTestBotAPI(t, func(method string, r *http.Request) any {
				form := cloneForm(t, r)
				if method != adminTestMethodSendMessage {
					t.Fatalf("unexpected telegram method: %s", method)
				}
				mu.Lock()
				replies = append(replies, form.Get("text"))
				mu.Unlock()
				return sentMessageResult(form)
			})

			ownerID := globalIndulgenceTestOwnerID
			switch {
			case tc.ownerID < 0:
				ownerID = 0
			case tc.ownerID > 0:
				ownerID = tc.ownerID
			}
			chatID, chatType := globalIndulgenceTestOwnerID, panelChatTypePrivate
			if tc.chatType != "" {
				chatID, chatType = tc.chatID, tc.chatType
			}
			admin := NewAdmin(testAdminService{db: store, bot: botAPI}, botAPI, store, store, forbiddenBanService{t: t}, ownerID)
			msg := adminCommandMessage(chatID, chatType, 50, tc.text(seed))
			if tc.senderID != 0 {
				msg.From.ID = tc.senderID
			}

			proceed, err := admin.Handle(ctx, &api.Update{Message: msg}, &msg.Chat, msg.From)
			if err != nil || proceed != tc.wantProceed {
				t.Fatalf("Handle = %v, %v; want proceed %v", proceed, err, tc.wantProceed)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.wantReply == nil {
				if len(replies) != 0 {
					t.Fatalf("ignored command replied: %q", replies)
				}
			} else {
				if len(replies) != 1 {
					t.Fatalf("replies = %q, want one", replies)
				}
				for _, want := range tc.wantReply(seed) {
					if !strings.Contains(replies[0], want) {
						t.Fatalf("reply %q does not contain %q", replies[0], want)
					}
				}
				for _, reject := range tc.rejectReply {
					if strings.Contains(replies[0], reject) {
						t.Fatalf("reply %q contains %q", replies[0], reject)
					}
				}
			}
			tc.check(t, store, seed)
		})
	}
}

func staticReply(parts ...string) func(globalIndulgenceSeed) []string {
	return func(globalIndulgenceSeed) []string { return parts }
}

func seedGlobalIndulgence(t *testing.T, store adminTestDB, empty bool) globalIndulgenceSeed {
	t.Helper()
	ctx := t.Context()
	var seed globalIndulgenceSeed
	perChat, err := store.CreateChatNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
		ChatID: -100, MatchType: db.NotSpammerMatchTypeUserID, MatchValue: "777002", CreatedByUserID: 1, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("seed per-chat entry: %v", err)
	}
	seed.perChat = perChat.ID
	if empty {
		return seed
	}
	for _, entry := range []struct {
		matchType string
		value     string
		target    *int64
	}{
		{db.NotSpammerMatchTypeUserID, "777001", &seed.globalUserID},
		{db.NotSpammerMatchTypeUsername, "spam_fighter", &seed.globalUsername},
	} {
		created, err := store.CreateGlobalNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
			MatchType: entry.matchType, MatchValue: entry.value, CreatedByUserID: globalIndulgenceTestOwnerID, CreatedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("seed global entry: %v", err)
		}
		*entry.target = created.ID
	}
	return seed
}

func requireGlobalEntry(t *testing.T, store adminTestDB, matchType, matchValue string) {
	t.Helper()
	entries, err := store.ListChatNotSpammerOverrides(t.Context(), 0, 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("global entries = %+v, %v; want one", entries, err)
	}
	entry := entries[0]
	if entry.ChatID != 0 || entry.MatchType != matchType || entry.MatchValue != matchValue || entry.CreatedByUserID != globalIndulgenceTestOwnerID {
		t.Fatalf("global entry = %+v", entry)
	}
}

func requireSeedGlobalEntriesKept(t *testing.T, store adminTestDB, _ globalIndulgenceSeed) {
	t.Helper()
	count, err := store.CountChatNotSpammerOverrides(t.Context(), 0)
	if err != nil || count != globalIndulgenceSeedEntries {
		t.Fatalf("global entries = %d, %v; want %d", count, err, globalIndulgenceSeedEntries)
	}
}
