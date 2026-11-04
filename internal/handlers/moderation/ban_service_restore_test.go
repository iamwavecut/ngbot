package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestUnmuteUserDoesNotReplayOldGroupDefaults(t *testing.T) {
	t.Parallel()
	var restored api.ChatPermissions
	photosAllowed := false
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case moderationTestTelegramMethodGetChatMember:
			return map[string]any{moderationTestJSONUser: map[string]any{"id": 200, moderationTestJSONIsBot: false, moderationTestJSONFirstName: moderationTestFirstNameUser}, moderationTestJSONStatus: moderationTestMemberStatusMember}
		case moderationTestTelegramMethodGetChat:
			return map[string]any{"id": -100, moderationTestJSONType: moderationTestSupergroup, moderationTestJSONPermissions: map[string]any{moderationTestJSONPermissionCanSendMessages: true, moderationTestJSONPermissionCanSendPhotos: photosAllowed}}
		case moderationTestTelegramMethodRestrictChatMember:
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &restored); err != nil {
				t.Fatal(err)
			}
			return true
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})
	store := &testBanStore{}
	service := &defaultBanService{bot: botAPI, db: store}
	if err := service.MuteUser(t.Context(), -100, 200, time.Now().Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	photosAllowed = true
	if err := service.UnmuteUser(t.Context(), -100, 200); err != nil {
		t.Fatal(err)
	}
	if !restored.CanSendPhotos {
		t.Fatal("old group photo restriction remained after the group allowed photos")
	}
}

func TestUnmuteUserClearsExpiredPersonalRestriction(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.November, 4, 12, 0, 0, 0, time.UTC)
	var restored api.ChatPermissions
	var restoredUntil int64
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		if method != moderationTestTelegramMethodRestrictChatMember {
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &restored); err != nil {
			t.Fatal(err)
		}
		restoredUntil, _ = strconv.ParseInt(r.Form.Get("until_date"), 10, 64)
		return true
	})
	store := &testBanStore{restriction: &db.UserRestriction{ChatID: -100, UserID: 200, ExpiresAt: now.Add(time.Minute), PriorPermissionsJSON: moderationTestPersonalPermissionsJSON, PriorUntilDate: now.Add(-time.Minute).Unix()}}
	service := &defaultBanService{bot: botAPI, db: store, now: func() time.Time { return now }}
	if err := service.UnmuteUser(t.Context(), -100, 200); err != nil {
		t.Fatal(err)
	}
	if !restored.CanSendPhotos || restoredUntil != 0 {
		t.Fatalf("expired restriction restored: photos=%t until=%d", restored.CanSendPhotos, restoredUntil)
	}
}

func TestUnmuteUserDefersNearExpiryUntilPriorDeadline(t *testing.T) {
	t.Parallel()
	for _, remaining := range []time.Duration{time.Second, 29 * time.Second, 30 * time.Second} {
		t.Run(remaining.String(), func(t *testing.T) {
			now := time.Date(2026, time.November, 4, 12, 0, 0, 0, time.UTC)
			calls := 0
			botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
				if method != moderationTestTelegramMethodRestrictChatMember {
					t.Fatalf("unexpected method: %s", method)
					return nil
				}
				calls++
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				var permissions api.ChatPermissions
				if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &permissions); err != nil {
					t.Fatal(err)
				}
				if !permissions.CanSendPhotos {
					t.Fatal("expired restriction was replayed on retry")
				}
				return true
			})
			store := &testBanStore{restriction: &db.UserRestriction{ChatID: -100, UserID: 200, PriorPermissionsJSON: moderationTestPersonalPermissionsJSON, PriorUntilDate: now.Add(remaining).Unix()}}
			service := &defaultBanService{bot: botAPI, db: store, now: func() time.Time { return now }}
			if err := service.UnmuteUser(t.Context(), -100, 200); err == nil {
				t.Fatal("near-expiry restoration must remain pending")
			}
			if calls != 0 || len(store.removed) != 0 {
				t.Fatalf("pending restoration changed Telegram or removed snapshot: calls=%d removed=%d", calls, len(store.removed))
			}
			now = now.Add(remaining)
			if err := service.UnmuteUser(t.Context(), -100, 200); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(store.removed) != 1 {
				t.Fatalf("expired restoration did not finish: calls=%d removed=%d", calls, len(store.removed))
			}
		})
	}
}

func TestUnmuteUserPreservesExistingPersonalRestrictions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.November, 4, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, status, permissions string
		until                     int64
		wantError, unrestricted   bool
	}{
		{name: "active restriction", status: telegramMemberRestricted, permissions: moderationTestPersonalPermissionsJSON, until: now.Add(time.Hour).Unix()},
		{name: "permanent restriction", status: telegramMemberRestricted, permissions: moderationTestPersonalPermissionsJSON},
		{name: "legacy restriction", permissions: moderationTestPersonalPermissionsJSON, until: now.Add(time.Hour).Unix()},
		{name: "legacy without snapshot", unrestricted: true},
		{name: "ordinary member", status: moderationTestMemberStatusMember, permissions: moderationTestPersonalPermissionsJSON, unrestricted: true},
		{name: "missing personal snapshot", status: telegramMemberRestricted, wantError: true},
		{name: "invalid personal snapshot", status: telegramMemberRestricted, permissions: `invalid`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
				if method != moderationTestTelegramMethodRestrictChatMember {
					t.Fatalf("unexpected method: %s", method)
					return nil
				}
				calls++
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				var permissions api.ChatPermissions
				if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &permissions); err != nil {
					t.Fatal(err)
				}
				until, _ := strconv.ParseInt(r.Form.Get("until_date"), 10, 64)
				if !permissions.CanSendMessages || permissions.CanSendPhotos != test.unrestricted || until != test.until {
					t.Errorf("restored rights: messages=%t photos=%t until=%d", permissions.CanSendMessages, permissions.CanSendPhotos, until)
				}
				return true
			})
			store := &testBanStore{restriction: &db.UserRestriction{ChatID: -100, UserID: 200, PriorMemberStatus: test.status, PriorPermissionsJSON: test.permissions, PriorUntilDate: test.until}}
			service := &defaultBanService{bot: botAPI, db: store, now: func() time.Time { return now }}
			err := service.UnmuteUser(t.Context(), -100, 200)
			if (err != nil) != test.wantError {
				t.Fatalf("restore error=%v, wantError=%t", err, test.wantError)
			}
			if test.wantError && (calls != 0 || len(store.removed) != 0) {
				t.Fatalf("invalid snapshot changed Telegram or persistence: calls=%d removed=%d", calls, len(store.removed))
			}
			if !test.wantError && (calls != 1 || len(store.removed) != 1) {
				t.Fatalf("restoration did not finish: calls=%d removed=%d", calls, len(store.removed))
			}
		})
	}
}

func TestUnmuteUserRetainsSnapshotAfterTelegramFailure(t *testing.T) {
	t.Parallel()
	botAPI := newModerationRetryTestBotAPI(t, func(method string, _ *http.Request) testAPIResponse {
		if method != moderationTestTelegramMethodRestrictChatMember {
			t.Fatalf("unexpected method: %s", method)
		}
		return testAPIResponse{OK: false, Description: "Bad Request: CHAT_ADMIN_REQUIRED"}
	})
	store := &testBanStore{restriction: &db.UserRestriction{ChatID: -100, UserID: 200, PriorMemberStatus: moderationTestMemberStatusMember}}
	service := &defaultBanService{bot: botAPI, db: store}
	if err := service.UnmuteUser(t.Context(), -100, 200); err == nil {
		t.Fatal("Telegram failure was ignored")
	}
	if len(store.removed) != 0 {
		t.Fatal("failed restoration lost the durable snapshot")
	}
}

func TestUnmuteUserRetainsCleanupWhenDeadlinePassesDuringTelegramRequest(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, time.November, 4, 12, 0, 0, 0, time.UTC)
	deadline := start.Add(45 * time.Second)
	calls := 0
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		if method != moderationTestTelegramMethodRestrictChatMember {
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
		calls++
		return true
	})
	store := &testBanStore{restriction: &db.UserRestriction{ChatID: -100, UserID: 200, PriorMemberStatus: telegramMemberRestricted, PriorPermissionsJSON: moderationTestPersonalPermissionsJSON, PriorUntilDate: deadline.Unix()}}
	reads := 0
	service := &defaultBanService{bot: botAPI, db: store, now: func() time.Time {
		reads++
		if reads == 1 {
			return start
		}
		return start.Add(20 * time.Second)
	}}
	if err := service.UnmuteUser(t.Context(), -100, 200); err == nil {
		t.Fatal("restoration lost cleanup after the request entered Telegram's permanent-restriction window")
	}
	if calls != 1 || len(store.removed) != 0 {
		t.Fatalf("cleanup snapshot not retained: calls=%d removed=%d", calls, len(store.removed))
	}
	service.now = func() time.Time { return deadline }
	if err := service.UnmuteUser(t.Context(), -100, 200); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(store.removed) != 1 {
		t.Fatalf("expired restoration did not complete: calls=%d removed=%d", calls, len(store.removed))
	}
}
