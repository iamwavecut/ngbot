package sqlite

import (
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestModerationActionFencePersistsStableDeadlineAndPhases(t *testing.T) {
	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	deadline := time.Date(2026, time.August, 13, 12, 10, 0, 0, time.UTC)
	action := &db.ModerationActionFence{ActionKey: "banlist:1", ChatID: -100, UserID: 200, MessageID: 42, BanUntil: deadline}
	claimed, err := client.BeginModerationAction(t.Context(), action, "owner-a", deadline.Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("begin action: %v", err)
	}
	if claimed.Status != db.ModerationActionStarted || claimed.Owner != "owner-a" || !claimed.BanUntil.Equal(deadline) {
		t.Fatalf("claimed action = %#v", claimed)
	}
	replayed, err := client.BeginModerationAction(t.Context(), &db.ModerationActionFence{ActionKey: action.ActionKey, ChatID: -100, UserID: 200, MessageID: 42, BanUntil: deadline.Add(time.Hour)}, "owner-b", deadline)
	if err != nil {
		t.Fatalf("replay action: %v", err)
	}
	if replayed.Owner != "owner-b" || replayed.EffectStartedAt.Valid || !replayed.BanUntil.Equal(deadline) {
		t.Fatalf("replay changed fence = %#v", replayed)
	}
	if ok, err := client.MarkModerationActionEffectStarted(t.Context(), action.ActionKey, "owner-b", deadline); err != nil || !ok {
		t.Fatalf("mark effect started: ok=%t err=%v", ok, err)
	}
	ambiguous, err := client.BeginModerationAction(t.Context(), action, "owner-c", deadline)
	if err != nil || ambiguous.Owner != "owner-b" || !ambiguous.EffectStartedAt.Valid {
		t.Fatalf("post-effect replay = %#v err=%v", ambiguous, err)
	}
	if ok, err := client.AdvanceModerationAction(t.Context(), action.ActionKey, "owner-b", db.ModerationActionStarted, db.ModerationActionBanned, "", deadline); err != nil || !ok {
		t.Fatalf("mark banned: ok=%t err=%v", ok, err)
	}
	if ok, err := client.AdvanceModerationAction(t.Context(), action.ActionKey, "owner-b", db.ModerationActionBanned, db.ModerationActionCompleted, "", deadline); err != nil || !ok {
		t.Fatalf("complete action: ok=%t err=%v", ok, err)
	}
}
