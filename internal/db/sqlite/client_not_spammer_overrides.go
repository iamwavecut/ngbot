package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamwavecut/ngbot/internal/db"
)

const normalizedOverrideScopeExpr = `COALESCE(NULLIF(CAST(chat_id AS TEXT), '0'), '')`

func (c *sqliteClient) CreateChatNotSpammerOverride(ctx context.Context, override *db.ChatNotSpammerOverride) (*db.ChatNotSpammerOverride, error) {
	if override == nil {
		return nil, fmt.Errorf("override is nil")
	}
	if override.ChatID <= 0 {
		return nil, fmt.Errorf("invalid chat id")
	}

	matchType, matchValue, err := db.NormalizeChatNotSpammerMatch(override.MatchType, override.MatchValue)
	if err != nil {
		return nil, err
	}
	override.MatchType = matchType
	override.MatchValue = matchValue

	c.mutex.Lock()
	defer c.mutex.Unlock()

	existing := &db.ChatNotSpammerOverride{}
	queryExisting := `
		SELECT id, COALESCE(chat_id, 0) AS chat_id, match_type, match_value, created_by_user_id, created_at
		FROM chat_not_spammer_overrides
		WHERE chat_id = ? AND match_type = ? AND match_value = ?
		ORDER BY id DESC
		LIMIT 1
	`
	if err := c.db.QueryRowxContext(ctx, queryExisting, override.ChatID, override.MatchType, override.MatchValue).StructScan(existing); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check existing chat not spammer override: %w", err)
	}

	query := `
		INSERT INTO chat_not_spammer_overrides (chat_id, match_type, match_value, created_by_user_id, created_at)
		VALUES (?, ?, ?, ?, ?)
	`
	result, err := c.db.ExecContext(ctx, query,
		override.ChatID,
		override.MatchType,
		override.MatchValue,
		override.CreatedByUserID,
		override.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat not spammer override: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to read chat not spammer override id: %w", err)
	}
	override.ID = id
	return override, nil
}

func (c *sqliteClient) GetChatNotSpammerOverride(ctx context.Context, chatID int64, id int64) (*db.ChatNotSpammerOverride, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, COALESCE(chat_id, 0) AS chat_id, match_type, match_value, created_by_user_id, created_at
		FROM chat_not_spammer_overrides
		WHERE id = ? AND chat_id = ?
	`
	override := &db.ChatNotSpammerOverride{}
	if err := c.db.QueryRowxContext(ctx, query, id, chatID).StructScan(override); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get chat not spammer override: %w", err)
	}
	return override, nil
}

func (c *sqliteClient) ListChatNotSpammerOverrides(ctx context.Context, chatID int64, limit int, offset int) ([]*db.ChatNotSpammerOverride, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, COALESCE(chat_id, 0) AS chat_id, match_type, match_value, created_by_user_id, created_at
		FROM chat_not_spammer_overrides
		WHERE chat_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`
	rows, err := c.db.QueryxContext(ctx, query, chatID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list chat not spammer overrides: %w", err)
	}
	defer rows.Close()

	var overrides []*db.ChatNotSpammerOverride
	for rows.Next() {
		override := &db.ChatNotSpammerOverride{}
		if err := rows.StructScan(override); err != nil {
			return nil, fmt.Errorf("failed to scan chat not spammer override: %w", err)
		}
		overrides = append(overrides, override)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate chat not spammer overrides: %w", err)
	}
	return overrides, nil
}

func (c *sqliteClient) CountChatNotSpammerOverrides(ctx context.Context, chatID int64) (int, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `SELECT COUNT(*) FROM chat_not_spammer_overrides WHERE chat_id = ?`
	var count int
	if err := c.db.QueryRowxContext(ctx, query, chatID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count chat not spammer overrides: %w", err)
	}
	return count, nil
}

func (c *sqliteClient) DeleteChatNotSpammerOverride(ctx context.Context, chatID int64, id int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `DELETE FROM chat_not_spammer_overrides WHERE id = ? AND chat_id = ?`, id, chatID); err != nil {
		return fmt.Errorf("failed to delete chat not spammer override: %w", err)
	}
	return nil
}

func (c *sqliteClient) IsChatNotSpammer(ctx context.Context, chatID int64, userID int64, username string) (bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	matchClauses := make([]string, 0, 2)
	args := make([]any, 0, 6)
	normalizedScope := strconv.FormatInt(chatID, 10)
	args = append(args, normalizedScope)

	if userID > 0 {
		matchClauses = append(matchClauses, `(match_type = ? AND match_value = ?)`)
		args = append(args, db.NotSpammerMatchTypeUserID, strconv.FormatInt(userID, 10))
	}

	normalizedUsername := db.NormalizeChatNotSpammerUsername(username)
	if normalizedUsername != "" {
		matchClauses = append(matchClauses, `(match_type = ? AND match_value = ?)`)
		args = append(args, db.NotSpammerMatchTypeUsername, normalizedUsername)
	}

	if len(matchClauses) == 0 {
		return false, nil
	}

	args = append(args, normalizedScope)

	query := fmt.Sprintf(`
		SELECT 1
		FROM chat_not_spammer_overrides
		WHERE %s IN (?, '')
		AND (%s)
		ORDER BY CASE WHEN %s = ? THEN 0 ELSE 1 END, id DESC
		LIMIT 1
	`, normalizedOverrideScopeExpr, strings.Join(matchClauses, " OR "), normalizedOverrideScopeExpr)

	var matched int
	if err := c.db.QueryRowxContext(ctx, query, args...).Scan(&matched); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to lookup chat not spammer override: %w", err)
	}
	return matched == 1, nil
}
