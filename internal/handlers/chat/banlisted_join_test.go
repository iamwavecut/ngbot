package handlers

import (
	"net/http"
	"slices"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

func TestBanlistedJoinThroughUpdateProcessor(t *testing.T) {
	t.Parallel()
	const (
		query   = testTelegramMethodJoinRequestQuery
		decline = testTelegramMethodDeclineJoinRequest
		ban     = testTelegramMethodBanChatMember
		member  = testTelegramMethodGetChatMember
	)
	for _, tc := range []struct {
		name                                                                       string
		queryID                                                                    string
		expired, missing, observedBan, legacyDecline, allowlisted, clean, noRights bool
		failFirst                                                                  string
		denied                                                                     string
		want                                                                       []string
	}{
		{name: "guard query", queryID: testJoinQueryID, want: []string{query, ban}},
		{name: "queued applicant", want: []string{decline, ban}},
		{name: "expired query and already handled request", queryID: testJoinQueryID, expired: true, missing: true, want: []string{query, decline, ban}},
		{name: "retry only decline", failFirst: decline, want: []string{decline, ban, decline}},
		{name: "retry unconfirmed ban", failFirst: ban, want: []string{decline, ban, member, ban}},
		{name: "ban confirmed on readback", failFirst: ban, observedBan: true, want: []string{decline, ban, member}},
		{name: "legacy completed decline", legacyDecline: true, want: []string{ban}},
		{name: "invite permission denied independently", denied: decline, want: []string{decline, ban}},
		{name: "ban permission denied independently", denied: ban, want: []string{decline, ban}},
		{name: "manual exception", allowlisted: true},
		{name: "clean applicant", clean: true},
		{name: "no restrict rights", noRights: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			client, err := sqlite.NewSQLiteClient(t.Context(), dir, "join.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			chat := api.Chat{ID: -100, Type: testChatTypeSupergroup}
			user := api.User{ID: 200}
			update := &api.Update{UpdateID: 19, ChatJoinRequest: &api.ChatJoinRequest{Chat: chat, From: user, UserChatID: 9300, QueryID: tc.queryID, Date: time.Now().Unix()}}
			if err := client.SetSettings(t.Context(), db.DefaultSettings(chat.ID)); err != nil {
				t.Fatal(err)
			}
			author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID}
			now := time.Now().UTC()
			if _, _, err := client.RecordSafeAuthorMessage(t.Context(), chat.ID, author, 33, now, 1, time.Hour, true); err != nil {
				t.Fatal(err)
			}
			if tc.allowlisted {
				if _, err := client.CreateChatNotSpammerOverride(t.Context(), &db.ChatNotSpammerOverride{ChatID: chat.ID, MatchType: db.NotSpammerMatchTypeUserID, MatchValue: "200", CreatedByUserID: 3}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.legacyDecline {
				action, err := client.BeginModerationAction(t.Context(), &db.ModerationActionFence{ActionKey: "banlist-join:19:-100:200", ChatID: chat.ID, UserID: user.ID, BanUntil: now}, "previous-release", now)
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := client.AdvanceModerationAction(t.Context(), action.ActionKey, action.Owner, db.ModerationActionStarted, db.ModerationActionCompleted, "", now); err != nil || !ok {
					t.Fatalf("prepare legacy decline: %t %v", ok, err)
				}
			}

			var calls []string
			botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
				calls = append(calls, method)
				if method == query {
					if r.FormValue("chat_join_request_query_id") != testJoinQueryID || r.FormValue("result") != bot.JoinRequestQueryResultDecline {
						t.Errorf("invalid query response: %v", r.Form)
					}
				} else if r.FormValue("chat_id") != "-100" || r.FormValue("user_id") != "200" {
					t.Errorf("wrong target for %s: %v", method, r.Form)
				}
				if method == ban && (r.FormValue("revoke_messages") != "true" || (r.FormValue("until_date") != "" && r.FormValue("until_date") != "0")) {
					t.Errorf("invalid permanent ban: %v", r.Form)
				}
				if method == tc.denied {
					return &testBotAPIError{code: 403, description: "not enough rights"}
				}
				if method == tc.failFirst && !slices.Contains(calls[:len(calls)-1], method) {
					return &testBotAPIError{code: 502, description: testTelegramErrorBadGateway}
				}
				if method == query && tc.expired {
					return &testBotAPIError{code: 400, description: "QUERY_ID_INVALID"}
				}
				if method == decline && tc.missing {
					return &testBotAPIError{code: 400, description: "HIDE_REQUESTER_MISSING"}
				}
				if method == member {
					status := testMemberStatusLeft
					if tc.observedBan {
						status = testMemberStatusKicked
					}
					return api.ChatMember{User: &user, Status: status}
				}
				return true
			})
			for attempt := range 2 {
				service := &testBanService{knownBanned: !tc.clean, moderationUnavailable: tc.noRights}
				processor := bot.NewUpdateProcessor(nil, NewModerationRouter(NewBanlistGuard(botAPI, client, service), nil))
				err := processor.Process(t.Context(), update)
				switch {
				case tc.denied != "":
					if err == nil || bot.ClassifyUpdateFailure(err).Disposition != bot.UpdateFailureTerminal {
						t.Fatalf("permission outcome: %v", err)
					}
				case attempt == 0 && tc.failFirst != "":
					if err == nil || bot.ClassifyUpdateFailure(err).Disposition != bot.UpdateFailureRetryable {
						t.Fatalf("retry outcome: %v", err)
					}
				case err != nil:
					t.Fatal(err)
				}
				if attempt == 0 {
					if err := client.Close(); err != nil {
						t.Fatal(err)
					}
					client, err = sqlite.NewSQLiteClient(t.Context(), dir, "join.db")
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if !slices.Equal(calls, tc.want) {
				t.Fatalf("Telegram calls %v, want %v", calls, tc.want)
			}
			trust, err := client.MessageTrust(t.Context(), chat.ID, author)
			wantTrusted := tc.allowlisted || tc.clean || tc.noRights || tc.denied == ban
			if err != nil || trust == nil || trust.TrustedUntil.Valid != wantTrusted {
				t.Fatalf("trusted=%+v want active=%t err=%v", trust, wantTrusted, err)
			}
		})
	}
}
