package bot_test

import (
	"context"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	log "github.com/sirupsen/logrus"
)

func TestServiceGetSettingsCreatesDefaults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	service := bot.NewService(ctx, &api.BotAPI{}, dbClient, log.NewEntry(log.New()))
	settings, err := service.GetSettings(ctx, -1001234567890)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings == nil {
		t.Fatalf("settings is nil")
	}

	expected := db.DefaultSettings(-1001234567890)
	if settings.ID != expected.ID {
		t.Fatalf("unexpected settings ID: got %d want %d", settings.ID, expected.ID)
	}
	if settings.Language != expected.Language {
		t.Fatalf("unexpected language: got %q want %q", settings.Language, expected.Language)
	}
	if settings.ChallengeTimeout != (3 * time.Minute).Nanoseconds() {
		t.Fatalf("unexpected challenge timeout: %d", settings.ChallengeTimeout)
	}
	if settings.RejectTimeout != (10 * time.Minute).Nanoseconds() {
		t.Fatalf("unexpected reject timeout: %d", settings.RejectTimeout)
	}
}

func TestServiceStartStop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}

	service := bot.NewService(ctx, &api.BotAPI{}, dbClient, log.NewEntry(log.New()))
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start service: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := service.Stop(stopCtx); err != nil {
		t.Fatalf("stop service: %v", err)
	}
}
