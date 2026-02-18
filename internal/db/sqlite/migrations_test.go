package sqlite

import (
	"context"
	"testing"
)

func TestSpamCasesIndexesExistAfterMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, err := NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	rows, err := client.db.QueryContext(ctx, "PRAGMA index_list('spam_cases')")
	if err != nil {
		t.Fatalf("query index_list: %v", err)
	}
	defer rows.Close()

	indexes := make(map[string]struct{})
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		indexes[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index rows: %v", err)
	}

	required := []string{"idx_spam_cases_chat_user", "idx_spam_cases_status"}
	for _, name := range required {
		if _, ok := indexes[name]; !ok {
			t.Fatalf("required index %q not found", name)
		}
	}
}
