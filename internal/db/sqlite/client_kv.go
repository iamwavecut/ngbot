package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *sqliteClient) GetKV(ctx context.Context, key string) (string, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var value string
	err := s.db.GetContext(ctx, &value, `SELECT value FROM kv_store WHERE key = ?`, key)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("failed to get value for key %s: %w", key, err)
	}
	return value, nil
}

func (s *sqliteClient) SetKV(ctx context.Context, key string, value string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO kv_store (key, value, updated_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET
		value = excluded.value,
		updated_at = excluded.updated_at
	`
	_, err := s.db.ExecContext(ctx, query, key, value)
	if err != nil {
		return fmt.Errorf("failed to set value for key %s: %w", key, err)
	}
	return nil
}
