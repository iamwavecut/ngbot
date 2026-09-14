package sqlite

import (
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"
	migrate "github.com/rubenv/sql-migrate"
)

func TestContextCleanupPendingPreventsIncompatibleMigrationRollback(t *testing.T) {
	t.Parallel()
	client := newAuthorTrustClient(t)
	now := time.Now()
	challenge, err := client.CreateChallenge(t.Context(), &db.Challenge{ChatID: -100, CommChatID: -100, UserID: 300, Status: db.ChallengeStatusRejectPending, ActionPhase: db.ChallengePhaseRejectContextPending, CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	source := &migrate.EmbedFileSystemMigrationSource{FileSystem: resources.FS, Root: migrationsRoot}
	if _, err := migrate.ExecMax(client.db.DB, "sqlite3", source, migrate.Down, 1); err == nil {
		t.Fatal("rollback stranded pending context cleanup in old application")
	}
	stored, err := client.GetChallengeByChatUser(t.Context(), -100, 300)
	if err != nil || stored == nil || stored.ChallengeID != challenge.ChallengeID || stored.ActionPhase != db.ChallengePhaseRejectContextPending {
		t.Fatalf("failed rollback changed cleanup state: stored=%+v err=%v", stored, err)
	}
}

func TestAuthorContextDeletionRollsBackAndPreservesScopeTrustAndBindings(t *testing.T) {
	t.Parallel()
	client := newAuthorTrustClient(t)
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-200)); err != nil {
		t.Fatal(err)
	}
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 300}
	now := time.Now().UTC()
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	records := []db.MessageContext{
		{ChatID: -100, MessageID: 1, AuthorKind: author.Kind, AuthorID: author.ID},
		{ChatID: -100, MessageID: 2, ThreadID: 20, AuthorKind: author.Kind, AuthorID: author.ID},
		{ChatID: -100, MessageID: 3, AuthorKind: db.MessageAuthorUser, AuthorID: 301},
		{ChatID: -100, MessageID: 4, AuthorKind: db.MessageAuthorSenderChat, AuthorID: -300},
		{ChatID: -200, MessageID: 1, AuthorKind: author.Kind, AuthorID: author.ID},
	}
	for i := range records {
		records[i].Text = "saved conversation"
		records[i].SentAt, records[i].UpdatedAt = now, now
		if err := client.UpsertMessageContext(t.Context(), &records[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.db.ExecContext(t.Context(), `CREATE TRIGGER reject_author_context_delete BEFORE DELETE ON chat_message_context WHEN OLD.chat_id = -100 BEGIN SELECT RAISE(ABORT, 'forced context deletion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteAuthorMessageContext(t.Context(), -100, author); err == nil {
		t.Fatal("expected deletion failure")
	}
	var tombstones int
	if err := client.db.GetContext(t.Context(), &tombstones, `SELECT COUNT(*) FROM chat_message_context_tombstones`); err != nil || tombstones != 0 {
		t.Fatalf("failed deletion left tombstones: count=%d err=%v", tombstones, err)
	}
	for _, record := range records {
		if saved, err := client.MessageContext(t.Context(), record.ChatID, record.MessageID); err != nil || saved == nil {
			t.Fatalf("rollback lost record: %+v err=%v", record, err)
		}
	}
	if _, err := client.db.ExecContext(t.Context(), `DROP TRIGGER reject_author_context_delete`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := client.DeleteAuthorMessageContext(t.Context(), -100, author); err != nil {
			t.Fatal(err)
		}
	}
	for i := range records {
		records[i].UpdatedAt = now.Add(time.Minute)
		if err := client.UpsertMessageContext(t.Context(), &records[i]); err != nil {
			t.Fatal(err)
		}
		saved, err := client.MessageContext(t.Context(), records[i].ChatID, records[i].MessageID)
		if err != nil || (saved == nil) != (i < 2) {
			t.Fatalf("deletion/replay scope is wrong: index=%d saved=%+v err=%v", i, saved, err)
		}
	}
	if checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1); err != nil || !checked {
		t.Fatalf("deletion removed checked binding: checked=%v err=%v", checked, err)
	}
	if trust, err := client.MessageTrust(t.Context(), -100, author); err != nil || trust == nil || !trust.Trusted(now) {
		t.Fatalf("context deletion changed trust: trust=%+v err=%v", trust, err)
	}
	if err := client.DeleteAuthorMessageContext(t.Context(), -100, db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: 300}); err == nil {
		t.Fatal("invalid typed author accepted")
	}
}

func TestTrustResetAndCaseClaimKeepUndeletedContext(t *testing.T) {
	t.Parallel()
	client := newAuthorTrustClient(t)
	now := time.Now()
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 300}
	if _, _, err := client.RecordSafeAuthorMessage(t.Context(), -100, author, 1, now, 1, time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: 1, AuthorKind: author.Kind, AuthorID: author.ID, Text: "not deleted by Telegram", SentAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := client.ResetMessageTrust(t.Context(), -100, author); err != nil {
		t.Fatal(err)
	}
	spamCase, err := client.CreateSpamCase(t.Context(), &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 2, CreatedAt: now, Status: db.SpamCaseStatusPending})
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 1); err != nil || saved == nil {
		t.Fatalf("pending case removed context: saved=%+v err=%v", saved, err)
	}
	if _, claimed, err := client.ClaimKnownSpamCase(t.Context(), spamCase.ID, now); err != nil || !claimed {
		t.Fatalf("claim failed: claimed=%v err=%v", claimed, err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 1); err != nil || saved == nil {
		t.Fatalf("claimed case removed context before enforcement: saved=%+v err=%v", saved, err)
	}
}
