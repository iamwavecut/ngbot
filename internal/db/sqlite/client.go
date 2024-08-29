package sqlite

import (
	"path/filepath"
	"sync"

	"github.com/iamwavecut/tool"

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

func NewSQLiteClient(dbPath string) *sqliteClient {
	dbx, err := sqlx.Open("sqlite", filepath.Join(infra.GetWorkDir(), dbPath))
	if err != nil {
		log.WithError(err).Fatalln("cant open db")
	}
	dbx.SetMaxOpenConns(42)

	migrationsSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       "migrations",
	}
	_, _, err = migrate.PlanMigration(dbx.DB, "sqlite3", migrationsSource, migrate.Up, 0)
	if err != nil {
		log.WithError(err).Fatalln("migrate plan failed")
	}

	n, err := migrate.Exec(dbx.DB, "sqlite3", migrationsSource, migrate.Up)
	if err != nil {
		log.WithError(err).WithField("migration", migrationsSource).Fatalln("migrate up failed")
	}
	if n > 0 {
		log.Infof("applied %d migrations!", n)
	}

	return &sqliteClient{db: dbx}
}

func (c *sqliteClient) GetSettings(chatID int64) (*db.Settings, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	res := &db.Settings{}
	return res, c.db.Get(res, "SELECT id, enabled, challenge_timeout, reject_timeout FROM chats WHERE id=?", chatID)
}

func (c *sqliteClient) SetSettings(settings *db.Settings) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	query := `
		INSERT INTO chats (id, enabled, challenge_timeout, reject_timeout) 
		VALUES (:id, :enabled, :challenge_timeout, :reject_timeout)
		ON CONFLICT(id) DO UPDATE SET 
		enabled=excluded.enabled, 
		challenge_timeout=excluded.challenge_timeout, 
		reject_timeout=excluded.reject_timeout;
	`
	return tool.Err(c.db.NamedExec(query, settings))
}

func (c *sqliteClient) InsertMember(chatID int64, userID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	_, err := c.db.Exec("INSERT OR IGNORE INTO chat_members (chat_id, user_id) VALUES (?, ?)", chatID, userID)
	return err
}

func (c *sqliteClient) InsertMembers(chatID int64, userIDs []int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	tx, err := c.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Preparex("INSERT OR IGNORE INTO chat_members (chat_id, user_id) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, userID := range userIDs {
		_, err = stmt.Exec(chatID, userID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (c *sqliteClient) DeleteMember(chatID int64, userID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	_, err := c.db.Exec("DELETE FROM chat_members WHERE chat_id = ? AND user_id = ?", chatID, userID)
	return err
}

func (c *sqliteClient) DeleteMembers(chatID int64, userIDs []int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	query, args, err := sqlx.In("DELETE FROM chat_members WHERE chat_id = ? AND user_id IN (?)", chatID, userIDs)
	if err != nil {
		return err
	}
	query = c.db.Rebind(query)
	_, err = c.db.Exec(query, args...)
	return err
}

func (c *sqliteClient) GetMembers(chatID int64) ([]int64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	var userIDs []int64
	err := c.db.Select(&userIDs, "SELECT user_id FROM chat_members WHERE chat_id = ?", chatID)
	return userIDs, err
}

func (c *sqliteClient) GetAllMembers() (map[int64][]int64, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	rows, err := c.db.Queryx("SELECT chat_id, user_id FROM chat_members")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := make(map[int64][]int64)
	for rows.Next() {
		var chatID int64
		var userID int64
		err = rows.Scan(&chatID, &userID)
		if err != nil {
			return nil, err
		}
		members[chatID] = append(members[chatID], userID)
	}

	return members, nil
}

func (c *sqliteClient) IsMember(chatID int64, userID int64) (bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	var count int
	err := c.db.Get(&count, "SELECT COUNT(*) FROM chat_members WHERE chat_id = ? AND user_id = ?", chatID, userID)
	return count > 0, err
}
