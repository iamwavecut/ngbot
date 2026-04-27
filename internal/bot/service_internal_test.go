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

	service := NewService(ctx, &api.BotAPI{}, dbClient, log.NewEntry(log.New()))
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
