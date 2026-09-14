package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

const messageContextColumns = `chat_id, message_id, thread_id, reply_to_message_id, author_kind, author_id, text, sent_at, updated_at, update_id`

func (c *sqliteClient) UpsertMessageContext(ctx context.Context, record *db.MessageContext) error {
	if record == nil || record.MessageID <= 0 || record.SentAt.IsZero() || record.UpdatedAt.IsZero() {
		return errors.New("invalid message context")
	}
	if err := (db.MessageAuthor{Kind: record.AuthorKind, ID: record.AuthorID}).Validate(); err != nil {
		return err
	}
	text := []rune(record.Text)
	if len(text) > 2000 {
		text = text[:2000]
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO chat_message_context (`+messageContextColumns+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM chat_message_context_tombstones WHERE chat_id = ? AND message_id = ?
		)
		ON CONFLICT(chat_id, message_id) DO UPDATE SET
			thread_id = excluded.thread_id,
			reply_to_message_id = excluded.reply_to_message_id,
			author_kind = excluded.author_kind,
			author_id = excluded.author_id,
			text = excluded.text,
			updated_at = excluded.updated_at,
			update_id = excluded.update_id
		WHERE excluded.updated_at > chat_message_context.updated_at
			OR (excluded.updated_at = chat_message_context.updated_at AND excluded.update_id > chat_message_context.update_id AND excluded.update_id > 0)
	`, record.ChatID, record.MessageID, record.ThreadID, record.ReplyToMessageID, record.AuthorKind, record.AuthorID, string(text), record.SentAt.UTC(), record.UpdatedAt.UTC(), record.UpdateID, record.ChatID, record.MessageID); err != nil {
		return fmt.Errorf("upsert message context: %w", err)
	}
	return nil
}

func (c *sqliteClient) MessageContext(ctx context.Context, chatID int64, messageID int) (*db.MessageContext, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	record := &db.MessageContext{}
	if err := c.db.GetContext(ctx, record, `
		SELECT `+messageContextColumns+` FROM chat_message_context WHERE chat_id = ? AND message_id = ?
	`, chatID, messageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read message context: %w", err)
	}
	return record, nil
}

func (c *sqliteClient) RecentMessageContext(ctx context.Context, chatID int64, threadID, beforeMessageID int, after time.Time, limit int) ([]db.MessageContext, error) {
	if limit <= 0 {
		return nil, errors.New("message context limit must be positive")
	}
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var records []db.MessageContext
	if err := c.db.SelectContext(ctx, &records, `
		SELECT `+messageContextColumns+` FROM chat_message_context
		WHERE chat_id = ? AND thread_id = ? AND message_id < ? AND sent_at >= ? AND text != ''
		ORDER BY message_id DESC LIMIT ?
	`, chatID, threadID, beforeMessageID, after.UTC(), limit); err != nil {
		return nil, fmt.Errorf("read recent message context: %w", err)
	}
	return records, nil
}

func (c *sqliteClient) DeleteMessageContext(ctx context.Context, chatID int64, messageID int) error {
	if messageID <= 0 {
		return errors.New("invalid message context ID")
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message context deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_message_context_tombstones (chat_id, message_id, deleted_at)
		SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM chats WHERE id = ?)
		ON CONFLICT(chat_id, message_id) DO NOTHING
	`, chatID, messageID, time.Now().UTC(), chatID); err != nil {
		return fmt.Errorf("tombstone deleted message context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_message_context WHERE chat_id = ? AND message_id = ?`, chatID, messageID); err != nil {
		return fmt.Errorf("delete message context: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit message context deletion: %w", err)
	}
	return nil
}

func (c *sqliteClient) DeleteAuthorMessageContext(ctx context.Context, chatID int64, author db.MessageAuthor) error {
	if err := author.Validate(); err != nil {
		return err
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin author context deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_message_context_tombstones (chat_id, message_id, deleted_at)
		SELECT chat_id, message_id, ? FROM chat_message_context
		WHERE chat_id = ? AND author_kind = ? AND author_id = ?
		ON CONFLICT(chat_id, message_id) DO NOTHING
	`, time.Now().UTC(), chatID, author.Kind, author.ID); err != nil {
		return fmt.Errorf("tombstone deleted author context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM chat_message_context WHERE chat_id = ? AND author_kind = ? AND author_id = ?
	`, chatID, author.Kind, author.ID); err != nil {
		return fmt.Errorf("delete author message context: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit author context deletion: %w", err)
	}
	return nil
}
