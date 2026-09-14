package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

func TestDeletedGroupCommandCannotReturnAsReplyContext(t *testing.T) {
	t.Parallel()
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	message := &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "/help", SentAt: now, UpdatedAt: now}
	if err := client.UpsertMessageContext(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	botAPI := newAdminTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != adminTestMethodDeleteMessage {
			t.Fatalf("unexpected method: %s", method)
		}
		return true
	})
	admin := &Admin{bot: botAPI, store: client}
	if err := admin.deleteGroupMessage(t.Context(), -100, 10); err != nil {
		t.Fatal(err)
	}
	message.UpdatedAt = now.Add(time.Second)
	if err := client.UpsertMessageContext(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved != nil {
		t.Fatalf("deleted command remained or replayed: saved=%+v err=%v", saved, err)
	}
}
