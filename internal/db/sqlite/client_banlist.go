package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"
)

const (
	banlistInsertBatchSize              = 1_000
	banlistCleanupBatchSize             = 10_000
	banlistVacuumChunkPages             = 1_024
	banlistVacuumMaximumPagesPerCleanup = 16_384
)

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
	defer func() { _ = stmt.Close() }()

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

func (s *sqliteClient) ApplyBanlistSource(
	ctx context.Context,
	provider, feedType, generation string,
	userIDs []int64,
	seenAt time.Time,
	expiresAt *time.Time,
	replace bool,
) (added, removed []int64, err error) {
	if provider == "" || feedType == "" || generation == "" {
		return nil, nil, errors.New("banlist provider, feed type, and generation must be set")
	}

	s.banlistImportMutex.Lock()
	defer s.banlistImportMutex.Unlock()

	conn, err := s.db.Connx(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire banlist import connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	generationID, err := s.createBanlistGeneration(ctx, conn, provider, feedType, generation, seenAt, expiresAt)
	if err != nil {
		return nil, nil, err
	}
	if err := s.insertBanlistEntries(ctx, conn, generationID, userIDs); err != nil {
		return nil, nil, err
	}

	incomingEffective := expiresAt == nil || expiresAt.After(seenAt)
	if err := stageBanlistDelta(ctx, conn, generationID, provider, feedType, seenAt, replace, incomingEffective); err != nil {
		return nil, nil, err
	}
	defer dropBanlistDeltaTables(ctx, conn)

	return s.activateBanlistGeneration(ctx, conn, generationID, incomingEffective)
}

func (s *sqliteClient) createBanlistGeneration(
	ctx context.Context,
	conn *sqlx.Conn,
	provider, feedType, generation string,
	seenAt time.Time,
	expiresAt *time.Time,
) (int64, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var generationID int64
	err := conn.QueryRowxContext(ctx, `
		INSERT INTO banlist_generations (
			provider, feed_type, generation, last_seen_at, expires_at, active
		) VALUES (?, ?, ?, ?, ?, 0)
		RETURNING id
	`, provider, feedType, generation, seenAt, expiresAt).Scan(&generationID)
	if err != nil {
		return 0, fmt.Errorf("create banlist generation %s/%s/%s: %w", provider, feedType, generation, err)
	}
	return generationID, nil
}

func (s *sqliteClient) insertBanlistEntries(ctx context.Context, conn *sqlx.Conn, generationID int64, userIDs []int64) error {
	for batch := range slices.Chunk(userIDs, banlistInsertBatchSize) {
		query := `INSERT OR IGNORE INTO banlist_entries (generation_id, user_id) VALUES ` + strings.TrimSuffix(strings.Repeat("(?, ?),", len(batch)), ",")
		args := make([]any, 0, len(batch)*2)
		for _, userID := range batch {
			args = append(args, generationID, userID)
		}

		s.mutex.Lock()
		_, err := conn.ExecContext(ctx, query, args...)
		s.mutex.Unlock()
		if err != nil {
			return fmt.Errorf("insert banlist generation %d entries: %w", generationID, err)
		}
	}
	return nil
}

func stageBanlistDelta(
	ctx context.Context,
	conn *sqlx.Conn,
	generationID int64,
	provider, feedType string,
	seenAt time.Time,
	replace, incomingEffective bool,
) error {
	for _, query := range []string{
		`CREATE TEMP TABLE IF NOT EXISTS temp_banlist_deactivated (generation_id INTEGER PRIMARY KEY) WITHOUT ROWID`,
		`CREATE TEMP TABLE IF NOT EXISTS temp_banlist_added (user_id INTEGER PRIMARY KEY) WITHOUT ROWID`,
		`CREATE TEMP TABLE IF NOT EXISTS temp_banlist_removed (user_id INTEGER PRIMARY KEY) WITHOUT ROWID`,
		`DELETE FROM temp_banlist_deactivated`,
		`DELETE FROM temp_banlist_added`,
		`DELETE FROM temp_banlist_removed`,
	} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("prepare banlist delta tables: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO temp_banlist_deactivated (generation_id)
		SELECT id
		FROM banlist_generations
		WHERE active = 1
			AND (
				(expires_at IS NOT NULL AND expires_at <= ?)
				OR (? AND provider = ? AND feed_type = ?)
			)
	`, seenAt, replace, provider, feedType); err != nil {
		return fmt.Errorf("stage deactivated banlist generations: %w", err)
	}

	if incomingEffective {
		if _, err := conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO temp_banlist_added (user_id)
			SELECT entry.user_id
			FROM banlist_entries AS entry
			LEFT JOIN banlist ON banlist.user_id = entry.user_id
			WHERE entry.generation_id = ? AND banlist.user_id IS NULL
		`, generationID); err != nil {
			return fmt.Errorf("stage added banlist ids: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO temp_banlist_removed (user_id)
		SELECT entry.user_id
		FROM banlist_entries AS entry
		JOIN temp_banlist_deactivated AS deactivated
			ON deactivated.generation_id = entry.generation_id
		WHERE (
			? = 0 OR NOT EXISTS (
				SELECT 1
				FROM banlist_entries AS incoming
				WHERE incoming.generation_id = ?
					AND incoming.user_id = entry.user_id
			)
		)
		AND NOT EXISTS (
			SELECT 1
			FROM banlist_entries AS remaining
			JOIN banlist_generations AS active_generation
				ON active_generation.id = remaining.generation_id
			LEFT JOIN temp_banlist_deactivated AS retired
				ON retired.generation_id = remaining.generation_id
			WHERE remaining.user_id = entry.user_id
				AND active_generation.active = 1
				AND retired.generation_id IS NULL
		)
	`, incomingEffective, generationID); err != nil {
		return fmt.Errorf("stage removed banlist ids: %w", err)
	}

	return nil
}

func (s *sqliteClient) activateBanlistGeneration(
	ctx context.Context,
	conn *sqlx.Conn,
	generationID int64,
	incomingEffective bool,
) (added, removed []int64, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin banlist activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE banlist_generations
		SET active = 0
		WHERE id IN (SELECT generation_id FROM temp_banlist_deactivated)
	`); err != nil {
		return nil, nil, fmt.Errorf("deactivate replaced banlist generations: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE banlist_generations
		SET active = ?
		WHERE id = ? AND active = 0
	`, incomingEffective, generationID)
	if err != nil {
		return nil, nil, fmt.Errorf("activate banlist generation %d: %w", generationID, err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return nil, nil, fmt.Errorf("read banlist generation activation: %w", err)
	} else if changed != 1 {
		return nil, nil, fmt.Errorf("banlist generation %d changed before activation", generationID)
	}

	removedRows, err := tx.QueryContext(ctx, `
		DELETE FROM banlist
		WHERE user_id IN (SELECT user_id FROM temp_banlist_removed)
		RETURNING user_id
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("remove ineffective banlist ids: %w", err)
	}
	removed, err = scanBanlistIDs(removedRows)
	if err != nil {
		return nil, nil, fmt.Errorf("read removed banlist ids: %w", err)
	}
	addedRows, err := tx.QueryContext(ctx, `
		INSERT OR IGNORE INTO banlist (user_id)
		SELECT user_id FROM temp_banlist_added
		RETURNING user_id
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("add effective banlist ids: %w", err)
	}
	added, err = scanBanlistIDs(addedRows)
	if err != nil {
		return nil, nil, fmt.Errorf("read added banlist ids: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit banlist activation: %w", err)
	}
	return added, removed, nil
}

func dropBanlistDeltaTables(ctx context.Context, conn *sqlx.Conn) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, table := range []string{"temp_banlist_deactivated", "temp_banlist_added", "temp_banlist_removed"} {
		_, _ = conn.ExecContext(cleanupCtx, `DROP TABLE IF EXISTS `+table)
	}
}

func scanBanlistIDs(rows *sql.Rows) ([]int64, error) {
	defer func() { _ = rows.Close() }()
	userIDs := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return userIDs, nil
}

func (s *sqliteClient) CleanupBanlistSources(ctx context.Context) error {
	s.banlistImportMutex.Lock()
	defer s.banlistImportMutex.Unlock()

	for {
		more, err := s.cleanupBanlistGenerationBatch(ctx)
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}
	return s.reclaimBanlistStorage(ctx)
}

func (s *sqliteClient) cleanupBanlistGenerationBatch(ctx context.Context) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin banlist generation cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var generationID int64
	err = tx.GetContext(ctx, &generationID, `
		SELECT id
		FROM banlist_generations
		WHERE active = 0
		ORDER BY id
		LIMIT 1
	`)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select inactive banlist generation: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		DELETE FROM banlist_entries
		WHERE generation_id = ?
			AND user_id IN (
				SELECT user_id
				FROM banlist_entries
				WHERE generation_id = ?
				LIMIT ?
			)
	`, generationID, generationID, banlistCleanupBatchSize)
	if err != nil {
		return false, fmt.Errorf("delete inactive banlist generation %d entries: %w", generationID, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read inactive banlist cleanup count: %w", err)
	}
	if deleted == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM banlist_generations WHERE id = ? AND active = 0`, generationID); err != nil {
			return false, fmt.Errorf("delete inactive banlist generation %d: %w", generationID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit banlist generation cleanup: %w", err)
	}
	return true, nil
}

func (s *sqliteClient) reclaimBanlistStorage(ctx context.Context) error {
	reclaimedPages := 0
	for reclaimedPages < banlistVacuumMaximumPagesPerCleanup {
		s.mutex.Lock()
		var before int
		err := s.db.GetContext(ctx, &before, `PRAGMA freelist_count`)
		if err == nil && before > 0 {
			_, err = s.db.ExecContext(ctx, `PRAGMA incremental_vacuum(1024)`)
		}
		var after int
		if err == nil {
			err = s.db.GetContext(ctx, &after, `PRAGMA freelist_count`)
		}
		s.mutex.Unlock()
		if err != nil {
			return fmt.Errorf("incremental vacuum after banlist cleanup: %w", err)
		}
		if before == 0 || after >= before {
			break
		}
		reclaimedPages += before - after
		if before-after < banlistVacuumChunkPages {
			break
		}
	}

	s.mutex.Lock()
	_, optimizeErr := s.db.ExecContext(ctx, `PRAGMA optimize`)
	s.mutex.Unlock()
	if optimizeErr != nil {
		return fmt.Errorf("optimize database after banlist cleanup: %w", optimizeErr)
	}

	var busy, logFrames, checkpointedFrames int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpoint WAL after banlist cleanup: %w", err)
	}
	log.WithFields(log.Fields{
		"reclaimed_pages":     reclaimedPages,
		"wal_busy":            busy,
		"wal_frames":          logFrames,
		"checkpointed_frames": checkpointedFrames,
	}).Debug("completed banlist storage maintenance")
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
	results := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		results[userID] = struct{}{}
	}
	return results, nil
}
