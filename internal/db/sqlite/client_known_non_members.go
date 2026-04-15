package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func (c *sqliteClient) IsChatKnownNonMember(ctx context.Context, chatID int64, userID int64) (bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var count int
	if err := c.db.GetContext(ctx, &count, `
		SELECT COUNT(*)
		FROM chat_known_non_members
		WHERE chat_id = ? AND user_id = ?
	`, chatID, userID); err != nil {
		return false, fmt.Errorf("failed to check known non-member: %w", err)
	}
	return count > 0, nil
}

func (c *sqliteClient) UpsertChatKnownNonMember(ctx context.Context, record *db.ChatKnownNonMember) error {
	if record == nil {
		return fmt.Errorf("record is nil")
	}
	if record.UserID <= 0 {
		return fmt.Errorf("invalid user id")
	}

	now := time.Now()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO chat_known_non_members (chat_id, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chat_id, user_id) DO UPDATE SET
			updated_at = excluded.updated_at
	`, record.ChatID, record.UserID, record.CreatedAt, record.UpdatedAt); err != nil {
		return fmt.Errorf("failed to upsert known non-member: %w", err)
	}
	return nil
}

func (c *sqliteClient) DeleteChatKnownNonMember(ctx context.Context, chatID int64, userID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `
		DELETE FROM chat_known_non_members
		WHERE chat_id = ? AND user_id = ?
	`, chatID, userID); err != nil {
		return fmt.Errorf("failed to delete known non-member: %w", err)
	}
	return nil
}

func (c *sqliteClient) getChatKnownNonMember(ctx context.Context, chatID int64, userID int64) (*db.ChatKnownNonMember, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	record := &db.ChatKnownNonMember{}
	if err := c.db.QueryRowxContext(ctx, `
		SELECT chat_id, user_id, created_at, updated_at
		FROM chat_known_non_members
		WHERE chat_id = ? AND user_id = ?
	`, chatID, userID).StructScan(record); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get known non-member: %w", err)
	}
	return record, nil
}
