package sqlite

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestMessageProbationPersistsOriginalDeadlineAndGraduation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	client, err := NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const (
		chatID = int64(-100)
		userID = int64(200)
	)
	if err := client.SetSettings(t.Context(), db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	startedAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	eligibleAt := startedAt.Add(3 * time.Hour)

	probation, created, err := client.GetOrCreateMessageProbation(t.Context(), chatID, userID, startedAt, eligibleAt)
	if err != nil {
		t.Fatalf("create message probation: %v", err)
	}
	if !created || !probation.StartedAt.Equal(startedAt) || !probation.EligibleAt.Equal(eligibleAt) {
		t.Fatalf("unexpected created probation: %#v created=%t", probation, created)
	}

	changedStart := startedAt.Add(time.Hour)
	probation, created, err = client.GetOrCreateMessageProbation(t.Context(), chatID, userID, changedStart, changedStart.Add(6*time.Hour))
	if err != nil {
		t.Fatalf("get existing message probation: %v", err)
	}
	if created || !probation.StartedAt.Equal(startedAt) || !probation.EligibleAt.Equal(eligibleAt) {
		t.Fatalf("existing deadline changed: %#v created=%t", probation, created)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("close client before restart: %v", err)
	}
	client, err = NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite client: %v", err)
	}
	probation, err = client.MessageProbation(t.Context(), chatID, userID)
	if err != nil {
		t.Fatalf("get probation after restart: %v", err)
	}
	if probation == nil || !probation.EligibleAt.Equal(eligibleAt) || probation.GraduatedAt.Valid {
		t.Fatalf("unexpected probation after restart: %#v", probation)
	}

	graduatedAt := eligibleAt.Add(time.Minute)
	if err := client.MarkMessageProbationGraduated(t.Context(), chatID, userID, graduatedAt); err != nil {
		t.Fatalf("graduate probation: %v", err)
	}
	if err := client.MarkMessageProbationGraduated(t.Context(), chatID, userID, graduatedAt.Add(time.Hour)); err != nil {
		t.Fatalf("repeat graduation: %v", err)
	}
	probation, err = client.MessageProbation(t.Context(), chatID, userID)
	if err != nil {
		t.Fatalf("get graduated probation: %v", err)
	}
	if !probation.GraduatedAt.Valid || !probation.GraduatedAt.Time.Equal(graduatedAt) {
		t.Fatalf("unexpected graduation: %#v", probation.GraduatedAt)
	}

	if _, err := client.db.ExecContext(t.Context(), `DELETE FROM chats WHERE id = ?`, chatID); err != nil {
		t.Fatalf("delete parent chat: %v", err)
	}
	probation, err = client.MessageProbation(t.Context(), chatID, userID)
	if err != nil {
		t.Fatalf("get cascaded probation: %v", err)
	}
	if probation != nil {
		t.Fatalf("probation survived chat cascade: %#v", probation)
	}
}

func TestGetOrCreateMessageProbationIsConcurrentSafe(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const (
		chatID = int64(-100)
		userID = int64(200)
	)
	if err := client.SetSettings(t.Context(), db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	startedAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	eligibleAt := startedAt.Add(3 * time.Hour)

	var created atomic.Int32
	var waitGroup sync.WaitGroup
	for range 16 {
		waitGroup.Go(func() {
			probation, inserted, createErr := client.GetOrCreateMessageProbation(t.Context(), chatID, userID, startedAt, eligibleAt)
			if createErr != nil {
				t.Errorf("get or create probation: %v", createErr)
				return
			}
			if probation == nil || !probation.EligibleAt.Equal(eligibleAt) {
				t.Errorf("unexpected probation: %#v", probation)
			}
			if inserted {
				created.Add(1)
			}
		})
	}
	waitGroup.Wait()
	if created.Load() != 1 {
		t.Fatalf("created count = %d, want 1", created.Load())
	}
}
