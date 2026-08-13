package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

type testBanStore struct {
	removed     [][2]int64
	restriction *db.UserRestriction
	addErr      error
}

func (s *testBanStore) GetKV(context.Context, string) (string, error) {
	return "", nil
}

func (s *testBanStore) SetKV(context.Context, string, string) error {
	return nil
}

func (s *testBanStore) ApplyBanlistSource(context.Context, string, string, string, []int64, time.Time, *time.Time, bool) ([]int64, []int64, error) {
	return nil, nil, nil
}

func (s *testBanStore) CleanupBanlistSources(context.Context) error {
	return nil
}

func (s *testBanStore) CleanupRetainedRecords(context.Context, time.Time, int) error {
	return nil
}

func (s *testBanStore) GetBanlist(context.Context) (map[int64]struct{}, error) {
	return nil, nil
}

func (s *testBanStore) AddRestriction(_ context.Context, restriction *db.UserRestriction) error {
	s.restriction = restriction
	return s.addErr
}

func TestMuteUserRestoresCapturedPermissionsWhenPersistenceFails(t *testing.T) {
	t.Parallel()

	var restrictions []api.ChatPermissions
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case "getChatMember":
			return map[string]any{
				"user":   map[string]any{"id": 200, "is_bot": false, "first_name": "User"},
				"status": "member",
			}
		case "getChat":
			return map[string]any{
				"id": -100, "type": "supergroup",
				"permissions": map[string]any{"can_send_messages": true, "can_send_photos": true},
			}
		case "restrictChatMember":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			var permissions api.ChatPermissions
			if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &permissions); err != nil {
				t.Fatalf("unmarshal permissions: %v", err)
			}
			restrictions = append(restrictions, permissions)
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	store := &testBanStore{addErr: errors.New("database unavailable")}
	service := &defaultBanService{bot: botAPI, db: store}

	err := service.MuteUser(t.Context(), -100, 200)
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(restrictions) != 2 {
		t.Fatalf("restriction calls = %d, want mute and restore", len(restrictions))
	}
	if !restrictions[1].CanSendMessages || !restrictions[1].CanSendPhotos {
		t.Fatalf("compensating permissions = %#v", restrictions[1])
	}
}

func (s *testBanStore) RemoveRestriction(_ context.Context, chatID int64, userID int64) error {
	s.removed = append(s.removed, [2]int64{chatID, userID})
	return nil
}

func (s *testBanStore) GetActiveRestriction(context.Context, int64, int64) (*db.UserRestriction, error) {
	if s.restriction != nil {
		return s.restriction, nil
	}
	return &db.UserRestriction{ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (s *testBanStore) RemoveExpiredRestrictions(context.Context) error {
	return nil
}

func TestUnmuteUserRestoresCapturedRestrictivePermissions(t *testing.T) {
	t.Parallel()

	var permissions api.ChatPermissions
	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		if method != "restrictChatMember" {
			t.Fatalf("unexpected bot method: %s", method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("chat_id"); got != "-100" {
			t.Fatalf("unexpected chat_id: %q", got)
		}
		if got := r.Form.Get(logFieldUserID); got != "200" {
			t.Fatalf("unexpected user_id: %q", got)
		}
		if got := r.Form.Get("until_date"); got != "" && got != strconv.FormatInt(0, 10) {
			t.Fatalf("unexpected until_date: %q", got)
		}
		if err := json.Unmarshal([]byte(r.Form.Get("permissions")), &permissions); err != nil {
			t.Fatalf("unmarshal permissions: %v", err)
		}
		return true
	})

	want := api.ChatPermissions{
		CanSendMessages:      true,
		CanSendPhotos:        true,
		CanInviteUsers:       true,
		CanReactToMessages:   true,
		CanSendVoiceNotes:    false,
		CanManageTopics:      false,
		CanPinMessages:       false,
		CanSendOtherMessages: false,
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	store := &testBanStore{restriction: &db.UserRestriction{
		ChatID:               -100,
		UserID:               200,
		ExpiresAt:            time.Now().Add(time.Minute),
		PriorPermissionsJSON: string(encoded),
	}}
	service := &defaultBanService{bot: botAPI, db: store}
	if err := service.UnmuteUser(context.Background(), -100, 200); err != nil {
		t.Fatalf("unmute user: %v", err)
	}

	if permissions != want {
		t.Fatalf("restored permissions = %#v, want %#v", permissions, want)
	}
}

func TestBanUserWithMessageRevokesMessages(t *testing.T) {
	t.Parallel()

	botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
		if method != testTelegramMethodBanChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("chat_id"); got != "-100" {
			t.Fatalf("chat_id = %q, want -100", got)
		}
		if got := r.Form.Get(logFieldUserID); got != "200" {
			t.Fatalf("user_id = %q, want 200", got)
		}
		if got := r.Form.Get("revoke_messages"); got != "true" {
			t.Fatalf("revoke_messages = %q, want true", got)
		}
		return true
	})

	service := &defaultBanService{bot: botAPI, db: &testBanStore{}}
	if err := service.BanUserWithMessage(context.Background(), -100, 200, 50); err != nil {
		t.Fatalf("ban user: %v", err)
	}
}

func TestModerationAvailabilityCachesRightsAndExplicitFailureWins(t *testing.T) {
	t.Parallel()

	getChatMemberCalls := 0
	botAPI := newModerationTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != "getChatMember" {
			t.Fatalf("unexpected bot method: %s", method)
		}
		getChatMemberCalls++
		return map[string]any{
			"user": map[string]any{
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
			},
			"status":               "administrator",
			"can_restrict_members": true,
		}
	})

	service := NewBanService(botAPI, &testBanStore{})
	for range 2 {
		available, err := service.ModerationAvailable(context.Background(), -100)
		if err != nil || !available {
			t.Fatalf("expected cached moderation rights: available=%t err=%v", available, err)
		}
	}
	if getChatMemberCalls != 1 {
		t.Fatalf("expected one Telegram capability lookup, got %d", getChatMemberCalls)
	}

	service.MarkModerationUnavailable(-100)
	available, err := service.ModerationAvailable(context.Background(), -100)
	if err != nil || available {
		t.Fatalf("explicit privilege failure did not override cached rights: available=%t err=%v", available, err)
	}
	if getChatMemberCalls != 1 {
		t.Fatalf("explicit failure unexpectedly repeated Telegram lookup: %d", getChatMemberCalls)
	}
}

func TestMutePrivilegeFailureImmediatelyDisablesModeration(t *testing.T) {
	t.Parallel()

	botAPI := newModerationRetryTestBotAPI(t, func(method string, _ *http.Request) testAPIResponse {
		switch method {
		case "getChatMember":
			return testAPIResponse{OK: true, Result: map[string]any{
				"user":   map[string]any{"id": 200, "is_bot": false, "first_name": "User"},
				"status": "member",
			}}
		case "getChat":
			return testAPIResponse{OK: true, Result: map[string]any{
				"id": -100, "type": "supergroup", "permissions": map[string]any{"can_send_messages": true},
			}}
		case "restrictChatMember":
			return testAPIResponse{OK: false, Description: "Bad Request: CHAT_ADMIN_REQUIRED"}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return testAPIResponse{OK: true}
		}
	})
	service := NewBanService(botAPI, &testBanStore{})

	err := service.MuteUser(context.Background(), -100, 200)
	if !errors.Is(err, ErrNoPrivileges) {
		t.Fatalf("MuteUser error = %v, want ErrNoPrivileges", err)
	}
	available, err := service.ModerationAvailable(context.Background(), -100)
	if err != nil || available {
		t.Fatalf("privilege failure did not disable moderation: available=%t err=%v", available, err)
	}
}
