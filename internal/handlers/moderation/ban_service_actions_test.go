package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

type testBanStore struct {
	removed [][2]int64
}

func (s *testBanStore) GetKV(context.Context, string) (string, error) {
	return "", nil
}

func (s *testBanStore) SetKV(context.Context, string, string) error {
	return nil
}

func (s *testBanStore) UpsertBanlist(context.Context, []int64) error {
	return nil
}

func (s *testBanStore) GetBanlist(context.Context) (map[int64]struct{}, error) {
	return nil, nil
}

func (s *testBanStore) AddRestriction(context.Context, *db.UserRestriction) error {
	return nil
}

func (s *testBanStore) RemoveRestriction(_ context.Context, chatID int64, userID int64) error {
	s.removed = append(s.removed, [2]int64{chatID, userID})
	return nil
}

func (s *testBanStore) GetActiveRestriction(context.Context, int64, int64) (*db.UserRestriction, error) {
	return &db.UserRestriction{ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestUnmuteUserSendsExplicitAllowPermissions(t *testing.T) {
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
		if got := r.Form.Get("user_id"); got != "200" {
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

	service := &defaultBanService{bot: botAPI, db: &testBanStore{}}
	if err := service.UnmuteUser(context.Background(), -100, 200); err != nil {
		t.Fatalf("unmute user: %v", err)
	}

	if !permissions.CanSendMessages ||
		!permissions.CanSendAudios ||
		!permissions.CanSendDocuments ||
		!permissions.CanSendPhotos ||
		!permissions.CanSendVideos ||
		!permissions.CanSendVideoNotes ||
		!permissions.CanSendVoiceNotes ||
		!permissions.CanSendPolls ||
		!permissions.CanSendOtherMessages ||
		!permissions.CanAddWebPagePreviews ||
		!permissions.CanChangeInfo ||
		!permissions.CanInviteUsers ||
		!permissions.CanPinMessages ||
		!permissions.CanManageTopics {
		t.Fatalf("expected all send permissions to be true, got %#v", permissions)
	}
}
