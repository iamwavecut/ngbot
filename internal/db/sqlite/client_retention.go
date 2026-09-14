package sqlite

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/jmoiron/sqlx"
)

const (
	processedRecentJoinerRetention = 30 * 24 * time.Hour
	terminalSpamCaseRetention      = 90 * 24 * time.Hour
	messageContextRetention        = 24 * time.Hour
	retentionCleanupBatchSize      = 500
)

type RetentionResult struct {
	ProcessedRecentJoiners   int
	TerminalSpamCases        int
	MessageContexts          int
	MessageContextTombstones int
}

func (c *sqliteClient) CleanupRetainedRecords(ctx context.Context, now time.Time, limit int) error {
	for {
		result, err := c.CleanupRetention(ctx, now, limit)
		if err != nil {
			return err
		}
		if result.ProcessedRecentJoiners < limit && result.TerminalSpamCases < limit && result.MessageContexts < limit && result.MessageContextTombstones < limit {
			return nil
		}
		if c.retentionCleanupBetweenBatches != nil {
			c.retentionCleanupBetweenBatches()
		}
		runtime.Gosched()
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func (c *sqliteClient) CleanupRetention(ctx context.Context, now time.Time, limit int) (RetentionResult, error) {
	if limit <= 0 {
		return RetentionResult{}, errors.New("retention cleanup limit must be positive")
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("begin retention cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result := RetentionResult{}
	result.ProcessedRecentJoiners, err = deleteRetentionBatch(ctx, tx, `
		DELETE FROM recent_joiners
		WHERE id IN (
			SELECT id
			FROM recent_joiners
			WHERE processed = TRUE AND joined_at <= ?
			ORDER BY joined_at, id
			LIMIT ?
		)
	`, now.Add(-processedRecentJoinerRetention), limit)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("clean processed recent joiners: %w", err)
	}

	result.TerminalSpamCases, err = deleteRetentionBatch(ctx, tx, `
		DELETE FROM spam_cases
		WHERE id IN (
			SELECT spam_case.id
			FROM spam_cases AS spam_case
			WHERE spam_case.status IN (?, ?, ?)
				AND spam_case.resolved_at IS NOT NULL
				AND spam_case.resolved_at <= ?
				AND NOT EXISTS (
					SELECT 1
					FROM spam_case_report_messages AS report
					WHERE report.case_id = spam_case.id
				)
			ORDER BY spam_case.resolved_at, spam_case.id
			LIMIT ?
		)
	`, db.SpamCaseStatusSpam, db.SpamCaseStatusFalsePositive, db.SpamCaseStatusNotEnforced, now.Add(-terminalSpamCaseRetention), limit)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("clean terminal spam cases: %w", err)
	}

	result.MessageContexts, err = deleteRetentionBatch(ctx, tx, `
		DELETE FROM chat_message_context
		WHERE (chat_id, message_id) IN (
			SELECT chat_id, message_id FROM chat_message_context
			WHERE sent_at < ? ORDER BY sent_at, chat_id, message_id LIMIT ?
		)
	`, now.UTC().Add(-messageContextRetention), limit)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("clean message context: %w", err)
	}

	result.MessageContextTombstones, err = deleteRetentionBatch(ctx, tx, `
		DELETE FROM chat_message_context_tombstones
		WHERE (chat_id, message_id) IN (
			SELECT chat_id, message_id FROM chat_message_context_tombstones
			WHERE deleted_at < ? ORDER BY deleted_at, chat_id, message_id LIMIT ?
		)
	`, now.UTC().Add(-messageContextRetention), limit)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("clean message context tombstones: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("commit retention cleanup: %w", err)
	}
	return result, nil
}

func deleteRetentionBatch(ctx context.Context, tx *sqlx.Tx, query string, args ...any) (int, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	return int(rows), err
}
