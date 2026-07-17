package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func (c *sqliteClient) MessageProbation(ctx context.Context, chatID int64, userID int64) (*db.MessageProbation, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.messageProbation(ctx, chatID, userID)
}

func (c *sqliteClient) GetOrCreateMessageProbation(
	ctx context.Context,
	chatID int64,
	userID int64,
	startedAt time.Time,
	eligibleAt time.Time,
) (*db.MessageProbation, bool, error) {
	probation, err := c.MessageProbation(ctx, chatID, userID)
	if err != nil || probation != nil {
		return probation, false, err
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	probation, err = c.messageProbation(ctx, chatID, userID)
	if err != nil || probation != nil {
		return probation, false, err
	}

	result, err := c.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO chat_message_probations (
			chat_id, user_id, started_at, eligible_at
		) VALUES (?, ?, ?, ?)
	`, chatID, userID, startedAt, eligibleAt)
	if err != nil {
		return nil, false, fmt.Errorf("create message probation: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read message probation insert result: %w", err)
	}

	probation, err = c.messageProbation(ctx, chatID, userID)
	if err != nil {
		return nil, false, err
	}
	if probation == nil {
		return nil, false, fmt.Errorf("message probation disappeared after insert")
	}
	return probation, inserted == 1, nil
}

func (c *sqliteClient) MarkMessageProbationGraduated(ctx context.Context, chatID int64, userID int64, graduatedAt time.Time) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE chat_message_probations
		SET graduated_at = ?
		WHERE chat_id = ? AND user_id = ? AND graduated_at IS NULL
	`, graduatedAt, chatID, userID)
	if err != nil {
		return fmt.Errorf("graduate message probation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read message probation graduation result: %w", err)
	}
	if affected == 1 {
		return nil
	}

	probation, err := c.messageProbation(ctx, chatID, userID)
	if err != nil {
		return err
	}
	if probation == nil {
		return fmt.Errorf("message probation not found")
	}
	if !probation.GraduatedAt.Valid {
		return fmt.Errorf("message probation was not graduated")
	}
	return nil
}

func (c *sqliteClient) messageProbation(ctx context.Context, chatID int64, userID int64) (*db.MessageProbation, error) {
	probation := &db.MessageProbation{}
	if err := c.db.QueryRowxContext(ctx, `
		SELECT chat_id, user_id, started_at, eligible_at, graduated_at
		FROM chat_message_probations
		WHERE chat_id = ? AND user_id = ?
	`, chatID, userID).StructScan(probation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get message probation: %w", err)
	}
	return probation, nil
}
