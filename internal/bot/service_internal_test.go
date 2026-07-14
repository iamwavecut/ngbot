package bot

import (
	"context"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	log "github.com/sirupsen/logrus"
)

func TestWarmupCacheUsesStoredMemberIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const (
		chatID = int64(-100123)
		userID = int64(777)
	)
	if err := dbClient.SetSettings(ctx, db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if err := dbClient.InsertMember(ctx, chatID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	if err := service.warmupCache(ctx); err != nil {
		t.Fatalf("warmup cache: %v", err)
	}

	if _, ok := service.memberCache[chatID][userID]; !ok {
		t.Fatalf("expected real user ID %d to be cached, got %#v", userID, service.memberCache[chatID])
	}
	if _, ok := service.memberCache[chatID][0]; ok {
		t.Fatalf("did not expect slice index 0 to be cached as a user ID")
	}
}

func TestSettingsCacheUsesIndependentSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const chatID = int64(-100456)
	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	first, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get first settings: %v", err)
	}
	first.Language = "ru"

	second, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get second settings: %v", err)
	}
	if second.Language == first.Language {
		t.Fatalf("caller mutation leaked into cache: got language %q", second.Language)
	}
}

func TestFailedSettingsWriteDoesNotPublishCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}

	const chatID = int64(-100789)
	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	before, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("prime settings cache: %v", err)
	}
	if err := dbClient.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	changed := cloneSettings(before)
	changed.Language = "ru"
	if err := service.SetSettings(ctx, changed); err == nil {
		t.Fatal("expected settings write to fail")
	}
	after, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("read cached settings after failed write: %v", err)
	}
	if after.Language != before.Language {
		t.Fatalf("failed write changed cache: got %q want %q", after.Language, before.Language)
	}
}

func TestWarmupDoesNotOverwriteNewerSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const chatID = int64(-100987)
	stored := db.DefaultSettings(chatID)
	stored.Language = "en"
	if err := dbClient.SetSettings(ctx, stored); err != nil {
		t.Fatalf("store settings: %v", err)
	}

	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	newer := cloneSettings(stored)
	newer.Language = "ru"
	service.settingsCache[chatID] = newer
	if err := service.warmupCache(ctx); err != nil {
		t.Fatalf("warmup cache: %v", err)
	}
	if got := service.settingsCache[chatID].Language; got != newer.Language {
		t.Fatalf("warmup overwrote newer cache: got %q want %q", got, newer.Language)
	}
}
