package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

const telegramUpdateColumns = `
	update_id, dispatch_key, payload, security_relevant, status, attempt_count,
	available_at, received_at, started_at, completed_at, last_error, outcome_source
`

func (c *sqliteClient) EnqueueTelegramUpdate(ctx context.Context, update *db.TelegramUpdate) (bool, error) {
	if update == nil {
		return false, fmt.Errorf("telegram update is nil")
	}
	if update.ReceivedAt.IsZero() {
		update.ReceivedAt = time.Now()
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, err := c.db.ExecContext(ctx, `
		INSERT INTO telegram_update_inbox (
			update_id, dispatch_key, payload, security_relevant, available_at, received_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(update_id) DO NOTHING
	`, update.UpdateID, update.DispatchKey, update.Payload, update.SecurityRelevant, update.ReceivedAt, update.ReceivedAt)
	if err != nil {
		return false, fmt.Errorf("enqueue telegram update: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read telegram update insert result: %w", err)
	}
	return rows == 1, nil
}

func (c *sqliteClient) ListRunnableTelegramUpdates(ctx context.Context, now time.Time, limit int) ([]*db.TelegramUpdate, error) {
	if limit < 1 {
		return nil, nil
	}
	updates := make([]*db.TelegramUpdate, 0, limit)
	err := c.db.SelectContext(ctx, &updates, `
		SELECT `+telegramUpdateColumns+`
		FROM telegram_update_inbox AS candidate
		WHERE candidate.status IN (?, ?)
			AND candidate.available_at <= ?
			AND NOT EXISTS (
				SELECT 1
				FROM telegram_update_inbox AS predecessor
				WHERE predecessor.dispatch_key = candidate.dispatch_key
					AND predecessor.update_id < candidate.update_id
					AND predecessor.status NOT IN (?, ?)
			)
		ORDER BY candidate.update_id
		LIMIT ?
	`, db.TelegramUpdateStatusPending, db.TelegramUpdateStatusRetry, now, db.TelegramUpdateStatusCompleted, db.TelegramUpdateStatusDeadLetter, limit)
	if err != nil {
		return nil, fmt.Errorf("list runnable telegram updates: %w", err)
	}
	return updates, nil
}

func (c *sqliteClient) NextTelegramUpdateAvailableAt(ctx context.Context) (time.Time, bool, error) {
	var next time.Time
	if err := c.db.GetContext(ctx, &next, `
		SELECT candidate.available_at
		FROM telegram_update_inbox AS candidate
		WHERE candidate.status IN (?, ?)
			AND NOT EXISTS (
				SELECT 1
				FROM telegram_update_inbox AS predecessor
				WHERE predecessor.dispatch_key = candidate.dispatch_key
					AND predecessor.update_id < candidate.update_id
					AND predecessor.status NOT IN (?, ?)
			)
		ORDER BY candidate.available_at
		LIMIT 1
	`, db.TelegramUpdateStatusPending, db.TelegramUpdateStatusRetry, db.TelegramUpdateStatusCompleted, db.TelegramUpdateStatusDeadLetter); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("read next telegram update availability: %w", err)
	}
	return next, true, nil
}

func (c *sqliteClient) TelegramUpdate(ctx context.Context, updateID int) (*db.TelegramUpdate, bool, error) {
	var update db.TelegramUpdate
	if err := c.db.GetContext(ctx, &update, `SELECT `+telegramUpdateColumns+` FROM telegram_update_inbox WHERE update_id = ?`, updateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read telegram update: %w", err)
	}
	return &update, true, nil
}

func (c *sqliteClient) ClaimTelegramUpdate(ctx context.Context, updateID int, now time.Time) (*db.TelegramUpdate, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, err := c.db.ExecContext(ctx, `
		UPDATE telegram_update_inbox AS candidate
		SET status = ?, attempt_count = attempt_count + 1, started_at = ?, last_error = ''
		WHERE candidate.update_id = ?
			AND candidate.status IN (?, ?)
			AND candidate.available_at <= ?
			AND NOT EXISTS (
				SELECT 1
				FROM telegram_update_inbox AS predecessor
				WHERE predecessor.dispatch_key = candidate.dispatch_key
					AND predecessor.update_id < candidate.update_id
					AND predecessor.status NOT IN (?, ?)
			)
	`, db.TelegramUpdateStatusProcessing, now, updateID, db.TelegramUpdateStatusPending, db.TelegramUpdateStatusRetry, now, db.TelegramUpdateStatusCompleted, db.TelegramUpdateStatusDeadLetter)
	if err != nil {
		return nil, false, fmt.Errorf("claim telegram update: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read telegram update claim result: %w", err)
	}
	if rows != 1 {
		return nil, false, nil
	}
	var update db.TelegramUpdate
	if err := c.db.GetContext(ctx, &update, `SELECT `+telegramUpdateColumns+` FROM telegram_update_inbox WHERE update_id = ?`, updateID); err != nil {
		return nil, false, fmt.Errorf("read claimed telegram update: %w", err)
	}
	return &update, true, nil
}

func (c *sqliteClient) ScheduleTelegramUpdateRetry(ctx context.Context, updateID int, nextAttemptAt time.Time, source, lastError string) (bool, error) {
	return c.transitionProcessingTelegramUpdate(ctx, updateID, db.TelegramUpdateStatusRetry, nextAttemptAt, source, lastError)
}

func (c *sqliteClient) CompleteTelegramUpdate(ctx context.Context, updateID int, source string, now time.Time) (bool, error) {
	return c.transitionProcessingTelegramUpdate(ctx, updateID, db.TelegramUpdateStatusCompleted, now, source, "")
}

func (c *sqliteClient) transitionProcessingTelegramUpdate(ctx context.Context, updateID int, status string, at time.Time, source, lastError string) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	var completedAt any
	if status == db.TelegramUpdateStatusCompleted {
		completedAt = at
	}
	result, err := c.db.ExecContext(ctx, `
		UPDATE telegram_update_inbox
		SET status = ?, available_at = ?, completed_at = ?, last_error = ?, outcome_source = ?
		WHERE update_id = ? AND status = ?
	`, status, at, completedAt, lastError, source, updateID, db.TelegramUpdateStatusProcessing)
	if err != nil {
		return false, fmt.Errorf("transition telegram update to %s: %w", status, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read telegram update transition result: %w", err)
	}
	return rows == 1, nil
}

func (c *sqliteClient) DeadLetterTelegramUpdate(ctx context.Context, updateID int, source, reason, lastError string, now time.Time) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin telegram update dead letter: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE telegram_update_inbox
		SET status = ?, completed_at = ?, last_error = ?, outcome_source = ?
		WHERE update_id = ? AND status = ?
	`, db.TelegramUpdateStatusDeadLetter, now, lastError, source, updateID, db.TelegramUpdateStatusProcessing)
	if err != nil {
		return false, fmt.Errorf("mark telegram update dead letter: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read telegram update dead letter result: %w", err)
	}
	if rows != 1 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO telegram_update_failures (
			update_id, dispatch_key, security_relevant, attempt_count,
			failure_source, failure_reason, last_error, created_at
		)
		SELECT update_id, dispatch_key, security_relevant, attempt_count, ?, ?, ?, ?
		FROM telegram_update_inbox
		WHERE update_id = ?
	`, source, reason, lastError, now, updateID); err != nil {
		return false, fmt.Errorf("record telegram update failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit telegram update dead letter: %w", err)
	}
	return true, nil
}

func (c *sqliteClient) RecoverTelegramUpdates(ctx context.Context, now time.Time) (int64, error) {
	return c.RecoverStaleTelegramUpdates(ctx, now, now)
}

func (c *sqliteClient) RecoverStaleTelegramUpdates(ctx context.Context, now, startedBefore time.Time) (int64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, err := c.db.ExecContext(ctx, `
		UPDATE telegram_update_inbox
		SET status = ?, available_at = ?, started_at = NULL,
			last_error = CASE WHEN last_error = '' THEN 'interrupted before terminal outcome' ELSE last_error END,
			outcome_source = 'restart'
		WHERE status = ? AND started_at <= ?
	`, db.TelegramUpdateStatusRetry, now, db.TelegramUpdateStatusProcessing, startedBefore)
	if err != nil {
		return 0, fmt.Errorf("recover telegram updates: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read telegram update recovery result: %w", err)
	}
	return rows, nil
}

func (c *sqliteClient) ListTelegramUpdateFailures(ctx context.Context, limit int) ([]*db.TelegramUpdateFailure, error) {
	if limit < 1 {
		return nil, nil
	}
	failures := make([]*db.TelegramUpdateFailure, 0, limit)
	if err := c.db.SelectContext(ctx, &failures, `
		SELECT id, update_id, dispatch_key, security_relevant, attempt_count,
			failure_source, failure_reason, last_error, created_at, resolved_at
		FROM telegram_update_failures
		WHERE resolved_at IS NULL
		ORDER BY created_at, update_id
		LIMIT ?
	`, limit); err != nil {
		return nil, fmt.Errorf("list telegram update failures: %w", err)
	}
	return failures, nil
}

func (c *sqliteClient) CleanupTelegramUpdates(ctx context.Context, completedBefore, deadLetterBefore time.Time) (int64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, err := c.db.ExecContext(ctx, `
		DELETE FROM telegram_update_inbox
		WHERE (status = ? AND completed_at < ?)
			OR (status = ? AND completed_at < ?)
	`, db.TelegramUpdateStatusCompleted, completedBefore, db.TelegramUpdateStatusDeadLetter, deadLetterBefore)
	if err != nil {
		return 0, fmt.Errorf("cleanup telegram updates: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read telegram update cleanup result: %w", err)
	}
	return rows, nil
}
