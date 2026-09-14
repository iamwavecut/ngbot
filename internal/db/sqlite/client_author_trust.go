package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/jmoiron/sqlx"
)

func (c *sqliteClient) MessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error) {
	if err := author.Validate(); err != nil {
		return nil, err
	}
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return readMessageTrust(ctx, c.db, chatID, author)
}

func (c *sqliteClient) EnsureMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error) {
	trust, err := c.MessageTrust(ctx, chatID, author)
	if err != nil || trust != nil {
		return trust, err
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO chat_author_trust (chat_id, author_kind, author_id)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_id, author_kind, author_id) DO NOTHING
	`, chatID, author.Kind, author.ID); err != nil {
		return nil, fmt.Errorf("ensure message trust: %w", err)
	}
	return readMessageTrust(ctx, c.db, chatID, author)
}

func (c *sqliteClient) RecordSafeAuthorMessage(
	ctx context.Context,
	chatID int64,
	author db.MessageAuthor,
	messageID int,
	now time.Time,
	requiredMessages int,
	trustDuration time.Duration,
	eligible bool,
) (*db.MessageTrust, bool, error) {
	if err := author.Validate(); err != nil {
		return nil, false, err
	}
	if messageID <= 0 || now.IsZero() || requiredMessages <= 0 || trustDuration <= 0 {
		return nil, false, errors.New("invalid safe message parameters")
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin safe message transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_author_trust (chat_id, author_kind, author_id)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_id, author_kind, author_id) DO NOTHING
	`, chatID, author.Kind, author.ID); err != nil {
		return nil, false, fmt.Errorf("ensure safe message author: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO chat_challenged_messages (chat_id, message_id, user_id, author_kind, challenged_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, message_id) DO NOTHING
	`, chatID, messageID, author.ID, author.Kind, now.UTC())
	if err != nil {
		return nil, false, fmt.Errorf("bind safe author message: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read safe message insert result: %w", err)
	}
	trust, err := readMessageTrust(ctx, tx, chatID, author)
	if err != nil {
		return nil, false, err
	}
	if trust == nil {
		return nil, false, errors.New("safe message author disappeared")
	}
	if inserted == 1 && eligible && !trust.Suspended {
		trust.SafeMessages = min(trust.SafeMessages+1, requiredMessages)
		if trust.TrustedUntil.Valid || trust.SafeMessages == requiredMessages {
			trust.SafeMessages = requiredMessages
			if !trust.Trusted(now) {
				trust.TrustedUntil = sql.NullTime{Time: now.UTC().Add(trustDuration), Valid: true}
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_author_trust SET safe_messages = ?, trusted_until = ?
			WHERE chat_id = ? AND author_kind = ? AND author_id = ?
		`, trust.SafeMessages, trust.TrustedUntil, chatID, author.Kind, author.ID); err != nil {
			return nil, false, fmt.Errorf("advance message trust: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit safe message transaction: %w", err)
	}
	return trust, inserted == 1, nil
}

func (c *sqliteClient) ResetMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) error {
	if err := author.Validate(); err != nil {
		return err
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `
		UPDATE chat_author_trust SET safe_messages = 0, trusted_until = NULL
		WHERE chat_id = ? AND author_kind = ? AND author_id = ?
	`, chatID, author.Kind, author.ID); err != nil {
		return fmt.Errorf("reset message trust: %w", err)
	}
	return nil
}

func readMessageTrust(ctx context.Context, queryer sqlx.QueryerContext, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error) {
	trust := &db.MessageTrust{}
	if err := sqlx.GetContext(ctx, queryer, trust, `
		SELECT trust.chat_id, trust.author_kind, trust.author_id, trust.safe_messages, trust.trusted_until,
			EXISTS (
				SELECT 1 FROM spam_cases AS spam_case
				WHERE spam_case.chat_id = trust.chat_id
					AND spam_case.author_kind = trust.author_kind
					AND spam_case.user_id = trust.author_id
					AND spam_case.status IN (?, ?, ?)
			) AS suspended
		FROM chat_author_trust AS trust
		WHERE trust.chat_id = ? AND trust.author_kind = ? AND trust.author_id = ?
	`, db.SpamCaseStatusPending, db.SpamCaseStatusResolvingSpam, db.SpamCaseStatusResolvingFalsePositive, chatID, author.Kind, author.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read message trust: %w", err)
	}
	return trust, nil
}
