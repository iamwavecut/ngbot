package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/resources"

	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"
	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

type sqliteClient struct {
	db    *sqlx.DB
	mutex sync.RWMutex
}

func NewSQLiteClient(ctx context.Context, dbPath string) *sqliteClient {
	dbx, err := sqlx.Open("sqlite", filepath.Join(infra.GetWorkDir(), dbPath))
	if err != nil {
		log.WithField("error", err.Error()).Fatal("Failed to open database")
	}
	dbx.SetMaxOpenConns(42)

	migrationsSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       "migrations",
	}

	if _, _, err := migrate.PlanMigration(dbx.DB, "sqlite3", migrationsSource, migrate.Up, 0); err != nil {
		log.WithField("error", err.Error()).Fatal("Failed to plan migration")
	}

	if n, err := migrate.Exec(dbx.DB, "sqlite3", migrationsSource, migrate.Up); err != nil {
		log.WithField("error", err.Error()).WithField("migration", migrationsSource).Fatal("Failed to execute migration")
	} else if n > 0 {
		log.Infof("Applied %d migrations", n)
	}

	return &sqliteClient{db: dbx}
}

func (c *sqliteClient) GetSettings(ctx context.Context, chatID int64) (*db.Settings, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	res := &db.Settings{}
	query := "SELECT id, language, enabled, challenge_timeout, reject_timeout FROM chats WHERE id = ?"
	err := c.db.QueryRowxContext(ctx, query, chatID).StructScan(res)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.WithField("chatID", chatID).Debug("No settings found for chat")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get settings for chat %d: %w", chatID, err)
	}

	log.WithFields(log.Fields{
		"chatID":   chatID,
		"settings": res,
	}).Debug("Successfully retrieved settings")
	return res, nil
}

func (c *sqliteClient) GetAllSettings(ctx context.Context) (map[int64]*db.Settings, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	query := "SELECT id, language, enabled, challenge_timeout, reject_timeout FROM chats"
	rows, err := c.db.QueryxContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all settings: %w", err)
	}
	defer rows.Close()

	res := make(map[int64]*db.Settings)
	for rows.Next() {
		var s db.Settings
		if err := rows.StructScan(&s); err != nil {
			return nil, fmt.Errorf("failed to scan settings: %w", err)
		}
		res[s.ID] = &s
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over settings rows: %w", err)
	}

	return res, nil
}

func (c *sqliteClient) SetSettings(ctx context.Context, settings *db.Settings) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO chats (id, language, enabled, challenge_timeout, reject_timeout) 
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET 
		language = ?,
		enabled = ?, 
		challenge_timeout = ?, 
		reject_timeout = ?
	`
	_, err := c.db.ExecContext(ctx, query,
		settings.ID, settings.Language, settings.Enabled, settings.ChallengeTimeout, settings.RejectTimeout,
		settings.Language, settings.Enabled, settings.ChallengeTimeout, settings.RejectTimeout)
	if err != nil {
		return fmt.Errorf("failed to set settings: %w", err)
	}
	return nil
}

func (c *sqliteClient) InsertMember(ctx context.Context, chatID, userID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, err := c.db.ExecContext(ctx, "INSERT OR IGNORE INTO chat_members (chat_id, user_id) VALUES (?, ?)", chatID, userID)
	return err
}

func (c *sqliteClient) InsertMembers(ctx context.Context, chatID int64, userIDs []int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Preparex("INSERT OR IGNORE INTO chat_members (chat_id, user_id) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, userID := range userIDs {
		if _, err = stmt.Exec(chatID, userID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (c *sqliteClient) DeleteMember(ctx context.Context, chatID, userID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, err := c.db.ExecContext(ctx, "DELETE FROM chat_members WHERE chat_id = ? AND user_id = ?", chatID, userID)
	return err
}

func (c *sqliteClient) DeleteMembers(ctx context.Context, chatID int64, userIDs []int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query, args, err := sqlx.In("DELETE FROM chat_members WHERE chat_id = ? AND user_id IN (?)", chatID, userIDs)
	if err != nil {
		return err
	}
	query = c.db.Rebind(query)
	_, err = c.db.ExecContext(ctx, query, args...)
	return err
}

func (c *sqliteClient) GetMembers(ctx context.Context, chatID int64) ([]int64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var userIDs []int64
	err := c.db.SelectContext(ctx, &userIDs, "SELECT user_id FROM chat_members WHERE chat_id = ?", chatID)
	return userIDs, err
}

func (c *sqliteClient) GetAllMembers(ctx context.Context) (map[int64][]int64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	rows, err := c.db.QueryxContext(ctx, "SELECT chat_id, user_id FROM chat_members")
	if err != nil {
		return nil, fmt.Errorf("failed to query settings: %w", err)
	}
	defer rows.Close()

	members := make(map[int64][]int64)
	for rows.Next() {
		var chatID, userID int64
		if err := rows.Scan(&chatID, &userID); err != nil {
			return nil, err
		}
		members[chatID] = append(members[chatID], userID)
	}

	return members, rows.Err()
}

func (c *sqliteClient) IsMember(ctx context.Context, chatID, userID int64) (bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var count int
	err := c.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM chat_members WHERE chat_id = ? AND user_id = ?", chatID, userID)
	return count > 0, err
}

func (c *sqliteClient) Close() error {
	return c.db.Close()
}

func (s *sqliteClient) AddRestriction(ctx context.Context, restriction *db.UserRestriction) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO user_restrictions (user_id, chat_id, restricted_at, expires_at, reason)
		VALUES (?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
		restriction.UserID,
		restriction.ChatID,
		restriction.RestrictedAt,
		restriction.ExpiresAt,
		restriction.Reason,
	)
	return err
}

func (s *sqliteClient) RemoveRestriction(ctx context.Context, chatID int64, userID int64) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `DELETE FROM user_restrictions WHERE chat_id = ? AND user_id = ?`
	_, err := s.db.ExecContext(ctx, query, chatID, userID)
	return err
}

func (s *sqliteClient) CreateSpamCase(ctx context.Context, sc *db.SpamCase) (*db.SpamCase, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO spam_cases (chat_id, user_id, message_text, created_at, channel_username, channel_post_id, 
			notification_message_id, status, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.ExecContext(ctx, query,
		sc.ChatID,
		sc.UserID,
		sc.MessageText,
		sc.CreatedAt,
		sc.ChannelUsername,
		sc.ChannelPostID,
		sc.NotificationMessageID,
		sc.Status,
		sc.ResolvedAt,
	)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	sc.ID = id
	return sc, nil
}

func (s *sqliteClient) UpdateSpamCase(ctx context.Context, sc *db.SpamCase) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		UPDATE spam_cases 
		SET channel_username = ?,
			channel_post_id = ?,
			notification_message_id = ?,
			status = ?,
			resolved_at = ?
		WHERE id = ?
	`
	_, err := s.db.ExecContext(ctx, query,
		sc.ChannelUsername,
		sc.ChannelPostID,
		sc.NotificationMessageID,
		sc.Status,
		sc.ResolvedAt,
		sc.ID,
	)
	return err
}

func (s *sqliteClient) GetSpamCase(ctx context.Context, id int64) (*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var sc db.SpamCase
	err := s.db.GetContext(ctx, &sc, `SELECT * FROM spam_cases WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *sqliteClient) GetActiveSpamCase(ctx context.Context, chatID, userID int64) (*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var sc db.SpamCase
	err := s.db.GetContext(ctx, &sc, `
		SELECT * FROM spam_cases 
		WHERE chat_id = ? 
		AND user_id = ? 
		AND status = 'pending' 
		AND resolved_at IS NULL
		ORDER BY created_at DESC 
		LIMIT 1
	`, chatID, userID)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sc, nil
}

func (s *sqliteClient) GetPendingSpamCases(ctx context.Context) ([]*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var cases []*db.SpamCase
	err := s.db.SelectContext(ctx, &cases, `
		SELECT * FROM spam_cases 
		WHERE status = 'pending' AND resolved_at IS NULL
		ORDER BY created_at DESC
	`)
	return cases, err
}

func (s *sqliteClient) AddSpamVote(ctx context.Context, vote *db.SpamVote) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO spam_votes (case_id, voter_id, vote, voted_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(case_id, voter_id) DO UPDATE SET
		vote = excluded.vote,
		voted_at = excluded.voted_at
	`
	_, err := s.db.ExecContext(ctx, query,
		vote.CaseID,
		vote.VoterID,
		vote.Vote,
		vote.VotedAt,
	)
	return err
}

func (s *sqliteClient) GetSpamVotes(ctx context.Context, caseID int64) ([]*db.SpamVote, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var votes []*db.SpamVote
	err := s.db.SelectContext(ctx, &votes, `
		SELECT * FROM spam_votes 
		WHERE case_id = ?
		ORDER BY voted_at ASC
	`, caseID)
	return votes, err
}

func (s *sqliteClient) GetActiveRestriction(ctx context.Context, chatID, userID int64) (*db.UserRestriction, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var restriction db.UserRestriction
	err := s.db.GetContext(ctx, &restriction, `
		SELECT * FROM user_restrictions 
		WHERE chat_id = ? AND user_id = ? AND expires_at > datetime('now')
		ORDER BY restricted_at DESC LIMIT 1
	`, chatID, userID)
	if err != nil {
		return nil, err
	}
	return &restriction, nil
}

func (s *sqliteClient) RemoveExpiredRestrictions(ctx context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `DELETE FROM user_restrictions WHERE expires_at <= datetime('now')`
	_, err := s.db.ExecContext(ctx, query)
	return err
}

func (s *sqliteClient) AddChatRecentJoiner(ctx context.Context, joiner *db.RecentJoiner) (*db.RecentJoiner, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO recent_joiners (chat_id, user_id, username, joined_at, join_message_id, processed, is_spammer)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.ExecContext(ctx, query,
		joiner.ChatID,
		joiner.UserID,
		joiner.Username,
		joiner.JoinedAt,
		joiner.JoinMessageID,
		joiner.Processed,
		joiner.IsSpammer,
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	joiner.ID = id
	return joiner, nil
}

func (s *sqliteClient) GetChatRecentJoiners(ctx context.Context, chatID int64) ([]*db.RecentJoiner, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var joiners []*db.RecentJoiner
	err := s.db.SelectContext(ctx, &joiners, `
		SELECT * FROM recent_joiners 
		WHERE chat_id = ?
		ORDER BY joined_at DESC
	`, chatID)
	return joiners, err
}

func (s *sqliteClient) GetUnprocessedRecentJoiners(ctx context.Context) ([]*db.RecentJoiner, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var joiners []*db.RecentJoiner
	err := s.db.SelectContext(ctx, &joiners, `
		SELECT * FROM recent_joiners 
		WHERE processed = FALSE
		ORDER BY joined_at ASC
	`)
	return joiners, err
}

func (s *sqliteClient) ProcessRecentJoiner(ctx context.Context, chatID int64, userID int64, isSpammer bool) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		UPDATE recent_joiners 
		SET processed = TRUE, is_spammer = ?
		WHERE chat_id = ? AND user_id = ?
	`
	_, err := s.db.ExecContext(ctx, query, isSpammer, chatID, userID)
	return err
}

func (s *sqliteClient) UpsertBanlist(ctx context.Context, userIDs []int64) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	rollback := true
	defer func() {
		if rollback {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				log.WithError(err).Error("failed to rollback transaction")
			}
		}
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO banlist (user_id) VALUES (?)
		ON CONFLICT(user_id) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, userID := range userIDs {
		if _, err := stmt.ExecContext(ctx, userID); err != nil {
			return fmt.Errorf("failed to insert user %d: %w", userID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	rollback = false
	return nil
}

func (s *sqliteClient) GetBanlist(ctx context.Context) (map[int64]struct{}, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var userIDs []int64
	err := s.db.SelectContext(ctx, &userIDs, `SELECT user_id FROM banlist`)
	if err != nil {
		return nil, err
	}
	results := make(map[int64]struct{})
	for _, userID := range userIDs {
		results[userID] = struct{}{}
	}
	return results, nil
}

func (s *sqliteClient) GetKV(ctx context.Context, key string) (string, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var value string
	err := s.db.GetContext(ctx, &value, `SELECT value FROM kv_store WHERE key = ?`, key)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("failed to get value for key %s: %w", key, err)
	}
	return value, nil
}

func (s *sqliteClient) SetKV(ctx context.Context, key string, value string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO kv_store (key, value, updated_at) 
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET 
		value = excluded.value,
		updated_at = excluded.updated_at
	`
	_, err := s.db.ExecContext(ctx, query, key, value)
	if err != nil {
		return fmt.Errorf("failed to set value for key %s: %w", key, err)
	}
	return nil
}
