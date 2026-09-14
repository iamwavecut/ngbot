package sqlite

import (
	"context"
	"fmt"

	"github.com/iamwavecut/ngbot/internal/db"
)

func (c *sqliteClient) RecordChallengedMessage(ctx context.Context, chatID int64, userID int64, messageID int) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO chat_challenged_messages (
			chat_id, message_id, user_id, author_kind, challenged_at
		) VALUES (?, ?, ?, 'user', CURRENT_TIMESTAMP)
	`, chatID, messageID, userID)
	if err != nil {
		return false, fmt.Errorf("record challenged message: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read challenged message insert result: %w", err)
	}
	return inserted == 1, nil
}

func (c *sqliteClient) IsChallengedMessage(ctx context.Context, chatID int64, userID int64, messageID int) (bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var exists bool
	err := c.db.GetContext(ctx, &exists, `
		SELECT EXISTS(
			SELECT 1
			FROM chat_challenged_messages
			WHERE chat_id = ? AND message_id = ? AND user_id = ? AND author_kind = 'user'
		)
	`, chatID, messageID, userID)
	if err != nil {
		return false, fmt.Errorf("check challenged message: %w", err)
	}
	return exists, nil
}

func (c *sqliteClient) IsCheckedAuthorMessage(ctx context.Context, chatID int64, author db.MessageAuthor, messageID int) (bool, error) {
	if err := author.Validate(); err != nil {
		return false, err
	}
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var exists bool
	if err := c.db.GetContext(ctx, &exists, `
		SELECT EXISTS(
			SELECT 1 FROM chat_challenged_messages
			WHERE chat_id = ? AND message_id = ? AND user_id = ? AND author_kind = ?
		)
	`, chatID, messageID, author.ID, author.Kind); err != nil {
		return false, fmt.Errorf("check author message binding: %w", err)
	}
	return exists, nil
}
