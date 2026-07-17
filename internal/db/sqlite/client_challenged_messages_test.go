package sqlite

import (
	"testing"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestChallengedMessagePersistsAndCascadesWithChat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	client, err := NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const (
		chatID    = int64(-100)
		userID    = int64(200)
		messageID = 300
	)
	if err := client.SetSettings(t.Context(), db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	inserted, err := client.RecordChallengedMessage(t.Context(), chatID, userID, messageID)
	if err != nil {
		t.Fatalf("record challenged message: %v", err)
	}
	if !inserted {
		t.Fatal("first challenged message was not inserted")
	}
	inserted, err = client.RecordChallengedMessage(t.Context(), chatID, userID, messageID)
	if err != nil {
		t.Fatalf("record duplicate challenged message: %v", err)
	}
	if inserted {
		t.Fatal("duplicate challenged message was inserted")
	}
	inserted, err = client.RecordChallengedMessage(t.Context(), chatID, userID, messageID+1)
	if err != nil {
		t.Fatalf("record second challenged message: %v", err)
	}
	if !inserted {
		t.Fatal("second challenged message was not inserted")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("close client before restart: %v", err)
	}
	client, err = NewSQLiteClient(t.Context(), dir, "test.db")
	if err != nil {
		t.Fatalf("reopen sqlite client: %v", err)
	}
	for _, test := range []struct {
		name      string
		chatID    int64
		userID    int64
		messageID int
		want      bool
	}{
		{name: "exact message", chatID: chatID, userID: userID, messageID: messageID, want: true},
		{name: "different user", chatID: chatID, userID: userID + 1, messageID: messageID},
		{name: "different message", chatID: chatID, userID: userID, messageID: messageID + 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := client.IsChallengedMessage(t.Context(), test.chatID, test.userID, test.messageID)
			if err != nil {
				t.Fatalf("check challenged message: %v", err)
			}
			if got != test.want {
				t.Fatalf("challenged = %t, want %t", got, test.want)
			}
		})
	}

	if _, err := client.db.ExecContext(t.Context(), `DELETE FROM chats WHERE id = ?`, chatID); err != nil {
		t.Fatalf("delete chat: %v", err)
	}
	challenged, err := client.IsChallengedMessage(t.Context(), chatID, userID, messageID)
	if err != nil {
		t.Fatalf("check cascaded message: %v", err)
	}
	if challenged {
		t.Fatal("challenged message survived parent chat deletion")
	}
}
