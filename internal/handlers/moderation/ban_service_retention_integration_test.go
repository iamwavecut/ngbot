package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

func TestBanServiceStartCleansRetentionAfterSQLiteReopen(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dataDir := t.TempDir()
	client, err := sqlite.NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	if err := client.SetSettings(ctx, db.DefaultSettings(-100)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	stale := time.Now().UTC().Add(-91 * 24 * time.Hour)
	spamCase, err := client.CreateSpamCase(ctx, &db.SpamCase{
		ChatID:            -100,
		UserID:            11,
		MessageID:         1,
		MessageText:       "stale terminal case",
		CreatedAt:         stale,
		PreVoteRestricted: true,
		Status:            db.SpamCaseStatusSpam,
		ResolvedAt:        &stale,
	})
	if err != nil {
		t.Fatalf("seed terminal spam case: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close sqlite client: %v", err)
	}

	reopened, err := sqlite.NewSQLiteClient(ctx, dataDir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	now := time.Now().UTC().Format(time.RFC3339)
	if err := reopened.SetKV(ctx, kvKeyLastDailyFetch, now); err != nil {
		t.Fatalf("seed daily fetch time: %v", err)
	}
	if err := reopened.SetKV(ctx, kvKeyLastHourlyFetch, now); err != nil {
		t.Fatalf("seed hourly fetch time: %v", err)
	}

	service := NewBanService(nil, reopened)
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start ban service: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	if _, err := reopened.GetSpamCase(ctx, spamCase.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale terminal case after startup = %v, want sql.ErrNoRows", err)
	}
}
