package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/pkg/errors"
)

func (c *sqliteClient) UpsertChatManager(ctx context.Context, manager *db.ChatManager) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO chat_managers (chat_id, user_id, can_manage_chat, can_promote_members, can_restrict_members, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, user_id) DO UPDATE SET
		can_manage_chat = ?,
		can_promote_members = ?,
		can_restrict_members = ?,
		updated_at = ?
	`
	_, err := c.db.ExecContext(ctx, query,
		manager.ChatID,
		manager.UserID,
		manager.CanManageChat,
		manager.CanPromoteMembers,
		manager.CanRestrictMembers,
		manager.UpdatedAt,
		manager.CanManageChat,
		manager.CanPromoteMembers,
		manager.CanRestrictMembers,
		manager.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert chat manager: %w", err)
	}
	return nil
}

func (c *sqliteClient) GetChatManager(ctx context.Context, chatID int64, userID int64) (*db.ChatManager, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT chat_id, user_id, can_manage_chat, can_promote_members, can_restrict_members, updated_at
		FROM chat_managers
		WHERE chat_id = ? AND user_id = ?
	`
	manager := &db.ChatManager{}
	if err := c.db.QueryRowxContext(ctx, query, chatID, userID).StructScan(manager); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get chat manager: %w", err)
	}
	return manager, nil
}

func (c *sqliteClient) SetChatBotMembership(ctx context.Context, membership *db.ChatBotMembership) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO chat_bot_membership (chat_id, is_member, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
		is_member = ?,
		updated_at = ?
	`
	_, err := c.db.ExecContext(ctx, query,
		membership.ChatID,
		membership.IsMember,
		membership.UpdatedAt,
		membership.IsMember,
		membership.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to set chat bot membership: %w", err)
	}
	return nil
}

func (c *sqliteClient) GetChatBotMembership(ctx context.Context, chatID int64) (*db.ChatBotMembership, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `SELECT chat_id, is_member, updated_at FROM chat_bot_membership WHERE chat_id = ?`
	membership := &db.ChatBotMembership{}
	if err := c.db.QueryRowxContext(ctx, query, chatID).StructScan(membership); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get chat bot membership: %w", err)
	}
	return membership, nil
}

func (c *sqliteClient) CreateAdminPanelSession(ctx context.Context, session *db.AdminPanelSession) (*db.AdminPanelSession, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO admin_panel_sessions (user_id, chat_id, page, state_json, message_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := c.db.ExecContext(ctx, query,
		session.UserID,
		session.ChatID,
		session.Page,
		session.StateJSON,
		session.MessageID,
		session.CreatedAt,
		session.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create admin panel session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to read admin panel session id: %w", err)
	}
	session.ID = id
	return session, nil
}

func (c *sqliteClient) GetAdminPanelSession(ctx context.Context, id int64) (*db.AdminPanelSession, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, user_id, chat_id, page, state_json, message_id, created_at, updated_at
		FROM admin_panel_sessions
		WHERE id = ?
	`
	session := &db.AdminPanelSession{}
	if err := c.db.QueryRowxContext(ctx, query, id).StructScan(session); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get admin panel session: %w", err)
	}
	return session, nil
}

func (c *sqliteClient) GetAdminPanelSessionByUserChat(ctx context.Context, userID int64, chatID int64) (*db.AdminPanelSession, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, user_id, chat_id, page, state_json, message_id, created_at, updated_at
		FROM admin_panel_sessions
		WHERE user_id = ? AND chat_id = ?
	`
	session := &db.AdminPanelSession{}
	if err := c.db.QueryRowxContext(ctx, query, userID, chatID).StructScan(session); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get admin panel session by user chat: %w", err)
	}
	return session, nil
}

func (c *sqliteClient) GetAdminPanelSessionByUserPage(ctx context.Context, userID int64, page string) (*db.AdminPanelSession, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, user_id, chat_id, page, state_json, message_id, created_at, updated_at
		FROM admin_panel_sessions
		WHERE user_id = ? AND page = ?
		ORDER BY updated_at DESC
		LIMIT 1
	`
	session := &db.AdminPanelSession{}
	if err := c.db.QueryRowxContext(ctx, query, userID, page).StructScan(session); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get admin panel session by user page: %w", err)
	}
	return session, nil
}

func (c *sqliteClient) UpdateAdminPanelSession(ctx context.Context, session *db.AdminPanelSession) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		UPDATE admin_panel_sessions
		SET page = ?, state_json = ?, message_id = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := c.db.ExecContext(ctx, query,
		session.Page,
		session.StateJSON,
		session.MessageID,
		session.UpdatedAt,
		session.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update admin panel session: %w", err)
	}
	return nil
}

func (c *sqliteClient) DeleteAdminPanelSession(ctx context.Context, id int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `DELETE FROM admin_panel_sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete admin panel session: %w", err)
	}
	return nil
}

func (c *sqliteClient) GetExpiredAdminPanelSessions(ctx context.Context, before time.Time) ([]*db.AdminPanelSession, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, user_id, chat_id, page, state_json, message_id, created_at, updated_at
		FROM admin_panel_sessions
		WHERE updated_at < ?
	`
	rows, err := c.db.QueryxContext(ctx, query, before)
	if err != nil {
		return nil, fmt.Errorf("failed to query expired admin panel sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*db.AdminPanelSession
	for rows.Next() {
		session := &db.AdminPanelSession{}
		if err := rows.StructScan(session); err != nil {
			return nil, fmt.Errorf("failed to scan expired session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate expired sessions: %w", err)
	}
	return sessions, nil
}

func (c *sqliteClient) CreateAdminPanelCommand(ctx context.Context, cmd *db.AdminPanelCommand) (*db.AdminPanelCommand, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO admin_panel_commands (session_id, payload, created_at)
		VALUES (?, ?, ?)
	`
	result, err := c.db.ExecContext(ctx, query, cmd.SessionID, cmd.Payload, cmd.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create admin panel command: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to read admin panel command id: %w", err)
	}
	cmd.ID = id
	return cmd, nil
}

func (c *sqliteClient) GetAdminPanelCommand(ctx context.Context, id int64) (*db.AdminPanelCommand, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `SELECT id, session_id, payload, created_at FROM admin_panel_commands WHERE id = ?`
	cmd := &db.AdminPanelCommand{}
	if err := c.db.QueryRowxContext(ctx, query, id).StructScan(cmd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get admin panel command: %w", err)
	}
	return cmd, nil
}

func (c *sqliteClient) DeleteAdminPanelCommandsBySession(ctx context.Context, sessionID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `DELETE FROM admin_panel_commands WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("failed to delete admin panel commands: %w", err)
	}
	return nil
}

func (c *sqliteClient) CreateChatSpamExample(ctx context.Context, example *db.ChatSpamExample) (*db.ChatSpamExample, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO chat_spam_examples (chat_id, text, created_by_user_id, created_at)
		VALUES (?, ?, ?, ?)
	`
	result, err := c.db.ExecContext(ctx, query, example.ChatID, example.Text, example.CreatedByUserID, example.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat spam example: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to read chat spam example id: %w", err)
	}
	example.ID = id
	return example, nil
}

func (c *sqliteClient) GetChatSpamExample(ctx context.Context, id int64) (*db.ChatSpamExample, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `SELECT id, chat_id, text, created_by_user_id, created_at FROM chat_spam_examples WHERE id = ?`
	example := &db.ChatSpamExample{}
	if err := c.db.QueryRowxContext(ctx, query, id).StructScan(example); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get chat spam example: %w", err)
	}
	return example, nil
}

func (c *sqliteClient) ListChatSpamExamples(ctx context.Context, chatID int64, limit int, offset int) ([]*db.ChatSpamExample, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `
		SELECT id, chat_id, text, created_by_user_id, created_at
		FROM chat_spam_examples
		WHERE chat_id = ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`
	rows, err := c.db.QueryxContext(ctx, query, chatID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list chat spam examples: %w", err)
	}
	defer rows.Close()

	var examples []*db.ChatSpamExample
	for rows.Next() {
		example := &db.ChatSpamExample{}
		if err := rows.StructScan(example); err != nil {
			return nil, fmt.Errorf("failed to scan chat spam example: %w", err)
		}
		examples = append(examples, example)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate chat spam examples: %w", err)
	}
	return examples, nil
}

func (c *sqliteClient) CountChatSpamExamples(ctx context.Context, chatID int64) (int, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := `SELECT COUNT(*) FROM chat_spam_examples WHERE chat_id = ?`
	var count int
	if err := c.db.QueryRowxContext(ctx, query, chatID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count chat spam examples: %w", err)
	}
	return count, nil
}

func (c *sqliteClient) DeleteChatSpamExample(ctx context.Context, id int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, err := c.db.ExecContext(ctx, `DELETE FROM chat_spam_examples WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete chat spam example: %w", err)
	}
	return nil
}
