package sqlite

import (
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestAdministratorAdmissionPersistsWithoutInventingSafeMessages(t *testing.T) {
	dir := t.TempDir()
	client, err := NewSQLiteClient(t.Context(), dir, "trust.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for _, chatID := range []int64{-100, -200} {
		if err := client.SetSettings(t.Context(), db.DefaultSettings(chatID)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 42}
	if err := client.GrantAdminMemberTrust(t.Context(), -100, 42, now, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = NewSQLiteClient(t.Context(), dir, "trust.db")
	if err != nil {
		t.Fatal(err)
	}
	trust, err := client.MessageTrust(t.Context(), -100, author)
	if err != nil || trust == nil || !trust.Trusted(now) || trust.SafeMessages != 0 || !trust.AdminAdmittedAt.Time.Equal(now) {
		t.Fatalf("persisted admission: %+v %v", trust, err)
	}
	if other, err := client.MessageTrust(t.Context(), -200, author); err != nil || other != nil {
		t.Fatalf("admission leaked into another chat: %+v %v", other, err)
	}
	if checked, err := client.IsCheckedAuthorMessage(t.Context(), -100, author, 1); err != nil || checked {
		t.Fatalf("admission invented a checked message: %t %v", checked, err)
	}
	if err := client.ResetMessageTrust(t.Context(), -100, author); err != nil {
		t.Fatal(err)
	}
	for _, date := range []time.Time{now, now.Add(-time.Minute)} {
		if err := client.GrantAdminMemberTrust(t.Context(), -100, 42, date, 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	trust, err = client.MessageTrust(t.Context(), -100, author)
	if err != nil || trust.Trusted(now) {
		t.Fatalf("replay restored revoked trust: %+v %v", trust, err)
	}
	if err := client.GrantAdminMemberTrust(t.Context(), -100, 42, now.Add(time.Hour), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	trust, err = client.MessageTrust(t.Context(), -100, author)
	if err != nil || !trust.Trusted(now.Add(time.Hour)) {
		t.Fatalf("new admission did not restore trust: %+v %v", trust, err)
	}
}
