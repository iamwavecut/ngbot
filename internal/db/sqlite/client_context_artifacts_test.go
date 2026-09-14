package sqlite

import "testing"

func TestContextDeletionAllowsNotificationInUnconfiguredChat(t *testing.T) {
	t.Parallel()
	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.DeleteMessageContext(t.Context(), -999999, 50); err != nil {
		t.Fatalf("deleted notification in unconfigured log chat: %v", err)
	}
}
