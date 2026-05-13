package handlers

import (
	"context"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

func TestApplyRecommendedProtectionSettings(t *testing.T) {
	t.Parallel()

	settings := db.DefaultSettings(42)
	settings.GatekeeperEnabled = false
	settings.GatekeeperCaptchaEnabled = false
	settings.GatekeeperGreetingEnabled = true
	settings.LLMFirstMessageEnabled = false
	settings.ReactionProfileCheckEnabled = false
	settings.CommunityVotingEnabled = true

	applyRecommendedProtectionSettings(settings)

	if !settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled {
		t.Fatalf("expected gatekeeper captcha baseline to be enabled: %#v", settings)
	}
	if settings.GatekeeperGreetingEnabled {
		t.Fatalf("expected greeting to be disabled in recommended baseline")
	}
	if settings.GetChallengeTimeout() != 3*time.Minute {
		t.Fatalf("unexpected challenge timeout: %s", settings.GetChallengeTimeout())
	}
	if settings.GetRejectTimeout() != 10*time.Minute {
		t.Fatalf("unexpected reject timeout: %s", settings.GetRejectTimeout())
	}
	if !settings.LLMFirstMessageEnabled {
		t.Fatalf("expected LLM first message to be enabled")
	}
	if !settings.ReactionProfileCheckEnabled {
		t.Fatalf("expected reaction profile check to be enabled")
	}
	if settings.CommunityVotingEnabled {
		t.Fatalf("expected community voting to be disabled")
	}
}

func TestHasRecommendedProtection(t *testing.T) {
	t.Parallel()

	state := &panelState{
		Features: panelFeatureFlags{
			GatekeeperEnabled:           true,
			GatekeeperCaptchaEnabled:    true,
			GatekeeperGreetingEnabled:   false,
			LLMFirstMessageEnabled:      true,
			ReactionProfileCheckEnabled: true,
			CommunityVotingEnabled:      false,
		},
		ChallengeTimeout: (3 * time.Minute).Nanoseconds(),
		RejectTimeout:    (10 * time.Minute).Nanoseconds(),
	}

	if !hasRecommendedProtection(state) {
		t.Fatalf("expected recommended protection to be detected")
	}

	state.Features.CommunityVotingEnabled = true
	if hasRecommendedProtection(state) {
		t.Fatalf("expected recommended protection to be false after enabling voting")
	}
}

func TestToggleFeatureTogglesReactionProfileCheck(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	settings := db.DefaultSettings(42)
	if err := client.SetSettings(ctx, settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	admin := &Admin{s: testAdminService{db: client}}
	state := newPanelState(7, 42, "chat", settings)
	session := &db.AdminPanelSession{ChatID: 42}

	if err := admin.toggleFeature(ctx, session, &state, panelFeatureReactionProfileCheck); err != nil {
		t.Fatalf("toggle reaction profile check: %v", err)
	}

	got, err := client.GetSettings(ctx, 42)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if got.ReactionProfileCheckEnabled {
		t.Fatal("expected reaction profile check to be disabled after toggle")
	}
	if state.Features.ReactionProfileCheckEnabled {
		t.Fatal("expected panel state to sync disabled reaction profile check")
	}
}

func TestRenderPanelFallsBackUnknownPageToHome(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	settings := db.DefaultSettings(42)
	if err := client.SetSettings(ctx, settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	admin := &Admin{s: testAdminService{db: client}, store: client}
	state := newPanelState(7, 42, "chat", settings)
	state.Page = panelPage("ReactionModeration")
	session := &db.AdminPanelSession{ID: 1, ChatID: 42}

	if _, _, err := admin.renderPanel(ctx, session, &state); err != nil {
		t.Fatalf("render panel: %v", err)
	}
	if state.Page != panelPageHome {
		t.Fatalf("expected unknown page to normalize to home, got %q", state.Page)
	}
}

func TestHasCustomizedSettings(t *testing.T) {
	t.Parallel()

	settings := db.DefaultSettings(42)
	state := newPanelState(7, 42, "chat", settings)

	if hasCustomizedSettings(&state) {
		t.Fatal("expected default settings to stay pristine")
	}

	state.GatekeeperGreetingText = "hello"
	if !hasCustomizedSettings(&state) {
		t.Fatal("expected greeting change to mark settings as customized")
	}
}

func TestRecommendedProtectionShowsOnlyOnFirstSettingsLaunch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	admin := &Admin{s: testAdminService{db: client}}
	state := newPanelState(7, 42, "chat", db.DefaultSettings(42))

	show, err := admin.shouldShowRecommendedProtection(ctx, &state)
	if err != nil {
		t.Fatalf("first shouldShowRecommendedProtection: %v", err)
	}
	if !show {
		t.Fatal("expected recommended protection on the first pristine launch")
	}

	show, err = admin.shouldShowRecommendedProtection(ctx, &state)
	if err != nil {
		t.Fatalf("second shouldShowRecommendedProtection: %v", err)
	}
	if show {
		t.Fatal("expected recommended protection to be hidden after the first launch")
	}
}

func TestRecommendedProtectionStaysHiddenAfterSettingsUpdate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	admin := &Admin{s: testAdminService{db: client}}
	settings := db.DefaultSettings(42)
	settings.Language = "ru"

	if err := admin.saveChatSettings(ctx, settings); err != nil {
		t.Fatalf("saveChatSettings: %v", err)
	}

	state := newPanelState(7, 42, "chat", settings)
	show, err := admin.shouldShowRecommendedProtection(ctx, &state)
	if err != nil {
		t.Fatalf("shouldShowRecommendedProtection after update: %v", err)
	}
	if show {
		t.Fatal("expected recommended protection to stay hidden after settings update")
	}
}

type testAdminService struct {
	db  db.Client
	bot *api.BotAPI
}

func (s testAdminService) GetBot() *api.BotAPI {
	return s.bot
}

func (s testAdminService) GetDB() db.Client {
	return s.db
}

func (s testAdminService) IsMember(context.Context, int64, int64) (bool, error) {
	return false, nil
}

func (s testAdminService) InsertMember(context.Context, int64, int64) error {
	return nil
}

func (s testAdminService) DeleteMember(context.Context, int64, int64) error {
	return nil
}

func (s testAdminService) GetSettings(ctx context.Context, chatID int64) (*db.Settings, error) {
	return s.db.GetSettings(ctx, chatID)
}

func (s testAdminService) SetSettings(ctx context.Context, settings *db.Settings) error {
	return s.db.SetSettings(ctx, settings)
}

func (s testAdminService) GetLanguage(ctx context.Context, chatID int64, _ *api.User) string {
	if s.db != nil {
		settings, err := s.db.GetSettings(ctx, chatID)
		if err == nil && settings != nil && settings.Language != "" {
			return settings.Language
		}
	}
	return "en"
}
