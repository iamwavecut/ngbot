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

const (
	migrationsRoot         = "migrations"
	walJournalMode         = "wal"
	journalSizeLimitBytes  = 64 << 20
	walAutoCheckpointPages = 1_000
	incrementalAutoVacuum  = 2
)

type sqliteClient struct {
	db                 *sqlx.DB
	mutex              sync.RWMutex
	banlistImportMutex sync.Mutex
}

func NewSQLiteClient(ctx context.Context, dataDir string, dbPath string) (*sqliteClient, error) {
	workDir, err := infra.EnsureDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("ensure data dir: %w", err)
	}

	dbFilePath := filepath.Join(workDir, dbPath)
	query := make(url.Values)
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", journalSizeLimitBytes))
	query.Add("_pragma", fmt.Sprintf("wal_autocheckpoint(%d)", walAutoCheckpointPages))
	query.Add("_pragma", "auto_vacuum(incremental)")
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     dbFilePath,
		RawQuery: query.Encode(),
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
	var journalMode string
	if err := dbx.GetContext(ctx, &journalMode, "PRAGMA journal_mode = WAL"); err != nil {
		return nil, fmt.Errorf("enable WAL journal mode: %w", err)
	}
	if journalMode != walJournalMode {
		return nil, fmt.Errorf("WAL journal mode is disabled: %s", journalMode)
	}
	var journalSizeLimit int64
	if err := dbx.GetContext(ctx, &journalSizeLimit, "PRAGMA journal_size_limit"); err != nil {
		return nil, fmt.Errorf("read journal size limit: %w", err)
	}
	if journalSizeLimit != journalSizeLimitBytes {
		return nil, fmt.Errorf("journal size limit is %d, want %d", journalSizeLimit, journalSizeLimitBytes)
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
	var autoVacuum int
	if err := dbx.GetContext(ctx, &autoVacuum, "PRAGMA auto_vacuum"); err != nil {
		return nil, fmt.Errorf("read auto vacuum mode: %w", err)
	}
	if autoVacuum != incrementalAutoVacuum {
		log.WithField("auto_vacuum", autoVacuum).Warn("incremental auto-vacuum is unavailable; run --database-maintenance during downtime")
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

func MaintainDatabase(ctx context.Context, dataDir string, dbPath string) error {
	workDir, err := infra.EnsureDir(dataDir)
	if err != nil {
		return fmt.Errorf("ensure data dir: %w", err)
	}
	dbFilePath := filepath.Join(workDir, dbPath)
	query := make(url.Values)
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn := (&url.URL{Scheme: "file", Path: dbFilePath, RawQuery: query.Encode()}).String()
	dbx, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open database %q for maintenance: %w", dbFilePath, err)
	}
	defer func() { _ = dbx.Close() }()
	dbx.SetMaxOpenConns(1)
	if err := dbx.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database %q for maintenance: %w", dbFilePath, err)
	}

	var busy, logFrames, checkpointedFrames int
	if err := dbx.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("truncate WAL before maintenance: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("database is busy; stop all other processes before maintenance")
	}
	var journalMode string
	if err := dbx.GetContext(ctx, &journalMode, `PRAGMA journal_mode = DELETE`); err != nil {
		return fmt.Errorf("switch to rollback journal for maintenance: %w", err)
	}
	if journalMode != "delete" {
		return fmt.Errorf("rollback journal mode is unavailable: %s", journalMode)
	}
	if _, err := dbx.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		return fmt.Errorf("enable incremental auto-vacuum: %w", err)
	}
	if _, err := dbx.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum database: %w", err)
	}
	if _, err := dbx.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return fmt.Errorf("optimize database: %w", err)
	}
	if err := validateSQLiteMaintenanceResult(ctx, dbx); err != nil {
		return err
	}
	if err := dbx.GetContext(ctx, &journalMode, `PRAGMA journal_mode = WAL`); err != nil {
		return fmt.Errorf("restore WAL journal mode: %w", err)
	}
	if journalMode != walJournalMode {
		return fmt.Errorf("WAL journal mode is unavailable after maintenance: %s", journalMode)
	}
	var journalSizeLimit int64
	if err := dbx.GetContext(ctx, &journalSizeLimit, fmt.Sprintf(`PRAGMA journal_size_limit = %d`, journalSizeLimitBytes)); err != nil {
		return fmt.Errorf("set journal size limit after maintenance: %w", err)
	}
	if journalSizeLimit != journalSizeLimitBytes {
		return fmt.Errorf("journal size limit is %d after maintenance, want %d", journalSizeLimit, journalSizeLimitBytes)
	}
	if err := os.Chmod(dbFilePath, 0o600); err != nil {
		return fmt.Errorf("secure database %q after maintenance: %w", dbFilePath, err)
	}
	return nil
}

func validateSQLiteMaintenanceResult(ctx context.Context, dbx *sqlx.DB) error {
	var autoVacuum int
	if err := dbx.GetContext(ctx, &autoVacuum, `PRAGMA auto_vacuum`); err != nil {
		return fmt.Errorf("read auto-vacuum mode after maintenance: %w", err)
	}
	if autoVacuum != incrementalAutoVacuum {
		return fmt.Errorf("auto-vacuum mode is %d after maintenance, want incremental", autoVacuum)
	}
	var quickCheck string
	if err := dbx.GetContext(ctx, &quickCheck, `PRAGMA quick_check`); err != nil {
		return fmt.Errorf("quick check after maintenance: %w", err)
	}
	if quickCheck != "ok" {
		return fmt.Errorf("quick check failed after maintenance: %s", quickCheck)
	}
	rows, err := dbx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check after maintenance: %w", err)
	}
	hasViolation := rows.Next()
	if closeErr := rows.Close(); closeErr != nil {
		return fmt.Errorf("close foreign key check after maintenance: %w", closeErr)
	}
	if hasViolation {
		return fmt.Errorf("foreign key check reported violations after maintenance")
	}
	return nil
}
