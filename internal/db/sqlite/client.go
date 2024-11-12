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
		log.WithError(err).Fatal("Failed to open database")
	}
	dbx.SetMaxOpenConns(42)

	migrationsSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       "migrations",
	}

	if _, _, err := migrate.PlanMigration(dbx.DB, "sqlite3", migrationsSource, migrate.Up, 0); err != nil {
		log.WithError(err).Fatal("Failed to plan migration")
	}

	if n, err := migrate.Exec(dbx.DB, "sqlite3", migrationsSource, migrate.Up); err != nil {
		log.WithError(err).WithField("migration", migrationsSource).Fatal("Failed to execute migration")
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
