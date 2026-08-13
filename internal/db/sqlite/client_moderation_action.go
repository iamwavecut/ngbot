package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func (c *sqliteClient) BeginModerationAction(ctx context.Context, action *db.ModerationActionFence, owner string, now time.Time) (*db.ModerationActionFence, error) {
	if action == nil || action.ActionKey == "" || owner == "" || action.BanUntil.IsZero() {
		return nil, fmt.Errorf("moderation action boundary is incomplete")
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin moderation action transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO moderation_action_fences (
			action_key, chat_id, user_id, message_id, status, ban_until, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(action_key) DO NOTHING
	`, action.ActionKey, action.ChatID, action.UserID, action.MessageID, db.ModerationActionPending, action.BanUntil, now, now); err != nil {
		return nil, fmt.Errorf("persist moderation action: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE moderation_action_fences
		SET status = ?, owner = ?, updated_at = ?
		WHERE action_key = ? AND (status = ? OR (status = ? AND effect_started_at IS NULL))
	`, db.ModerationActionStarted, owner, now, action.ActionKey, db.ModerationActionPending, db.ModerationActionStarted); err != nil {
		return nil, fmt.Errorf("claim moderation action: %w", err)
	}
	var result db.ModerationActionFence
	if err := tx.GetContext(ctx, &result, `SELECT * FROM moderation_action_fences WHERE action_key = ?`, action.ActionKey); err != nil {
		return nil, fmt.Errorf("read moderation action: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit moderation action: %w", err)
	}
	return &result, nil
}

func (c *sqliteClient) MarkModerationActionEffectStarted(ctx context.Context, actionKey, owner string, now time.Time) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, err := c.db.ExecContext(ctx, `
		UPDATE moderation_action_fences
		SET effect_started_at = ?, updated_at = ?
		WHERE action_key = ? AND owner = ? AND status = ? AND effect_started_at IS NULL
	`, now, now, actionKey, owner, db.ModerationActionStarted)
	if err != nil {
		return false, fmt.Errorf("mark moderation effect started: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (c *sqliteClient) AdvanceModerationAction(ctx context.Context, actionKey, owner, expectedStatus, nextStatus, lastError string, now time.Time) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, err := c.db.ExecContext(ctx, `
		UPDATE moderation_action_fences
		SET status = ?, last_error = ?, updated_at = ?
		WHERE action_key = ? AND owner = ? AND status = ?
	`, nextStatus, lastError, now, actionKey, owner, expectedStatus)
	if err != nil {
		return false, fmt.Errorf("advance moderation action: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read moderation action result: %w", err)
	}
	return rows == 1, nil
}
