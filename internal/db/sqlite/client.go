package sqlite

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/resources"

	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"
	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

const migrationsRoot = "migrations"

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
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     dbFilePath,
		RawQuery: "_pragma=foreign_keys(1)",
	}).String()
	dbx, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", dbFilePath, err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = dbx.Close()
		}
	}()
	dbx.SetMaxOpenConns(42)
	if err := dbx.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping database %q: %w", dbFilePath, err)
	}
	if err := os.Chmod(dbFilePath, 0o600); err != nil {
		return nil, fmt.Errorf("secure database %q: %w", dbFilePath, err)
	}
	var foreignKeysEnabled int
	if err := dbx.GetContext(ctx, &foreignKeysEnabled, "PRAGMA foreign_keys"); err != nil {
		return nil, fmt.Errorf("read foreign key mode: %w", err)
	}
	if foreignKeysEnabled != 1 {
		return nil, fmt.Errorf("foreign key enforcement is disabled")
	}

	migrationsSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       migrationsRoot,
	}

	if _, _, err := migrate.PlanMigration(dbx.DB, "sqlite3", migrationsSource, migrate.Up, 0); err != nil {
		return nil, fmt.Errorf("plan migrations: %w", err)
	}

	if n, err := migrate.Exec(dbx.DB, "sqlite3", migrationsSource, migrate.Up); err != nil {
		return nil, fmt.Errorf("execute migrations: %w", err)
	} else if n > 0 {
		log.Infof("Applied %d migrations", n)
	}
	rows, err := dbx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, fmt.Errorf("check foreign keys: %w", err)
	}
	hasViolation := rows.Next()
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("close foreign key check: %w", closeErr)
	}
	if hasViolation {
		return nil, fmt.Errorf("foreign key check reported violations")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	closeOnError = false
	return &sqliteClient{db: dbx}, nil
}

func (c *sqliteClient) Close() error {
	return c.db.Close()
}
