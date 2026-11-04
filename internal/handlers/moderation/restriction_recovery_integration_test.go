package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

func TestFalsePositiveRestorationWaitsForPriorExpiryAcrossRestart(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	deadline := now.Add(20 * time.Second)
	calls := 0
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case moderationTestTelegramMethodGetChatMember:
			return map[string]any{moderationTestJSONUser: map[string]any{"id": 1, moderationTestJSONIsBot: true, moderationTestJSONFirstName: moderationTestFirstNameBot}, moderationTestJSONStatus: moderationTestMemberStatusAdministrator, "can_restrict_members": true}
		case moderationTestTelegramMethodRestrictChatMember:
			calls++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			var permissions map[string]bool
			if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &permissions); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"can_send_messages", "can_send_audios", "can_send_documents", moderationTestJSONPermissionCanSendPhotos, "can_send_videos", "can_send_video_notes", "can_send_voice_notes", "can_send_polls", "can_react_to_messages", "can_send_other_messages", "can_add_web_page_previews", "can_edit_tag", "can_change_info", "can_invite_users", "can_pin_messages", "can_manage_topics"} {
				if !permissions[name] {
					t.Errorf("personal restriction not removed: %s", name)
				}
			}
			if date := r.Form.Get("until_date"); date != "" && date != "0" {
				t.Errorf("expired deadline restored: %s", date)
			}
			return true
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})
	dir := t.TempDir()
	client, err := sqlite.NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	restriction := &db.UserRestriction{ChatID: -100, UserID: 200, RestrictedAt: now, ExpiresAt: now.Add(5 * time.Minute), Reason: "Spam suspect", PriorMemberStatus: telegramMemberRestricted, PriorPermissionsJSON: moderationTestPersonalPermissionsJSON, PriorUntilDate: deadline.Unix()}
	if err := client.AddRestriction(t.Context(), restriction); err != nil {
		t.Fatal(err)
	}
	entry, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: 200, MessageID: 40, CreatedAt: now, Status: db.SpamCaseStatusResolvingFalsePositive, PreVoteRestricted: true, AttemptCount: maxSpamResolutionAttempts - 1})
	if err != nil {
		t.Fatal(err)
	}
	service := &defaultBanService{bot: botAPI, db: client, now: func() time.Time { return now }}
	control := &SpamControl{bot: botAPI, store: client, banService: service}
	if err := control.resolveClaimedCase(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	pending, err := client.GetSpamCase(t.Context(), entry.ID)
	if err != nil || pending.Status != db.SpamCaseStatusResolvingFalsePositive || !pending.NextAttemptAt.Valid || !pending.NextAttemptAt.Time.Equal(deadline) || calls != 0 {
		t.Fatalf("restoration was not deferred durably: case=%+v calls=%d err=%v", pending, calls, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = sqlite.NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	now = deadline
	service = &defaultBanService{bot: botAPI, db: client, now: func() time.Time { return now }}
	control = &SpamControl{bot: botAPI, store: client, banService: service}
	due, err := client.GetDueSpamCases(t.Context(), now)
	if err != nil || len(due) != 1 {
		t.Fatalf("restart lost deferred case: due=%+v err=%v", due, err)
	}
	if err := control.resolveClaimedCase(t.Context(), due[0]); err != nil {
		t.Fatal(err)
	}
	resolved, err := client.GetSpamCase(t.Context(), entry.ID)
	if err != nil || resolved.Status != db.SpamCaseStatusFalsePositive || resolved.NextAttemptAt.Valid || calls != 1 {
		t.Fatalf("deferred restoration did not finish: case=%+v calls=%d err=%v", resolved, calls, err)
	}
	stored, err := client.GetRestriction(t.Context(), -100, 200)
	if err != nil || stored != nil {
		t.Fatalf("restored snapshot remained: snapshot=%+v err=%v", stored, err)
	}
}
