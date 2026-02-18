package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

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

func NewSQLiteClient(ctx context.Context, dataDir string, dbPath string) (*sqliteClient, error) {
	workDir, err := infra.EnsureDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("ensure data dir: %w", err)
	}

	dbFilePath := filepath.Join(workDir, dbPath)
	dbx, err := sqlx.Open("sqlite", dbFilePath)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbFilePath, err)
	}
	dbx.SetMaxOpenConns(42)

	migrationsSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       "migrations",
	}

	if _, _, err := migrate.PlanMigration(dbx.DB, "sqlite3", migrationsSource, migrate.Up, 0); err != nil {
		return nil, fmt.Errorf("plan migrations: %w", err)
	}

	if n, err := migrate.Exec(dbx.DB, "sqlite3", migrationsSource, migrate.Up); err != nil {
		return nil, fmt.Errorf("execute migrations: %w", err)
	} else if n > 0 {
		log.Infof("Applied %d migrations", n)
	}

	select {
	case <-ctx.Done():
		_ = dbx.Close()
		return nil, ctx.Err()
	default:
	}

	return &sqliteClient{db: dbx}, nil
}

func (c *sqliteClient) Close() error {
	return c.db.Close()
}
