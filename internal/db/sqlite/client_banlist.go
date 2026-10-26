package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func (s *sqliteClient) BanlistSnapshots(ctx context.Context) ([]db.BanlistSnapshot, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var snapshots []db.BanlistSnapshot
	if err := s.db.SelectContext(ctx, &snapshots, `
		SELECT source, format, id_count, payload, payload_sha256, etag, last_modified, frozen, changed_at, checked_at
		FROM banlist_snapshots
		ORDER BY source
	`); err != nil {
		return nil, fmt.Errorf("load banlist snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *sqliteClient) SaveBanlistSnapshot(ctx context.Context, snapshot *db.BanlistSnapshot) error {
	payload := snapshot.Payload
	if payload == nil {
		payload = []byte{}
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin banlist snapshot %s: %w", snapshot.Source, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO banlist_snapshots (
			source, format, id_count, payload, payload_sha256, etag, last_modified, frozen, changed_at, checked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source) DO UPDATE SET
			format = excluded.format,
			id_count = excluded.id_count,
			payload = excluded.payload,
			payload_sha256 = excluded.payload_sha256,
			etag = excluded.etag,
			last_modified = excluded.last_modified,
			frozen = excluded.frozen,
			changed_at = excluded.changed_at,
			checked_at = excluded.checked_at
	`, snapshot.Source, snapshot.Format, snapshot.IDCount, payload, snapshot.PayloadSHA256, snapshot.ETag, snapshot.LastModified,
		snapshot.Frozen, snapshot.ChangedAt.UTC(), snapshot.CheckedAt.UTC()); err != nil {
		return fmt.Errorf("save banlist snapshot %s: %w", snapshot.Source, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit banlist snapshot %s: %w", snapshot.Source, err)
	}
	return nil
}

func (s *sqliteClient) TouchBanlistSnapshot(
	ctx context.Context,
	source string,
	etag, lastModified sql.NullString,
	checkedAt time.Time,
) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	result, err := s.db.ExecContext(ctx, `
		UPDATE banlist_snapshots
		SET etag = ?, last_modified = ?, checked_at = ?
		WHERE source = ?
	`, etag, lastModified, checkedAt.UTC(), source)
	if err != nil {
		return false, fmt.Errorf("touch banlist snapshot %s: %w", source, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read banlist snapshot %s touch: %w", source, err)
	}
	return changed == 1, nil
}

func (s *sqliteClient) BanlistObservations(ctx context.Context, now time.Time) ([]db.BanlistObservation, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var observations []db.BanlistObservation
	if err := s.db.SelectContext(ctx, &observations, `
		SELECT user_id, source, observed_at, expires_at
		FROM banlist_observations
		WHERE expires_at > ?
		ORDER BY source, user_id
	`, now.UTC()); err != nil {
		return nil, fmt.Errorf("load banlist observations: %w", err)
	}
	return observations, nil
}

func (s *sqliteClient) UpsertBanlistObservations(ctx context.Context, observations []db.BanlistObservation) error {
	if len(observations) == 0 {
		return nil
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin banlist observations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PreparexContext(ctx, `
		INSERT INTO banlist_observations (user_id, source, observed_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, source) DO UPDATE SET
			observed_at = excluded.observed_at,
			expires_at = MAX(expires_at, excluded.expires_at)
	`)
	if err != nil {
		return fmt.Errorf("prepare banlist observations: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, observation := range observations {
		if _, err := stmt.ExecContext(ctx, observation.UserID, observation.Source, observation.ObservedAt.UTC(), observation.ExpiresAt.UTC()); err != nil {
			return fmt.Errorf("upsert banlist observation %s/%d: %w", observation.Source, observation.UserID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit banlist observations: %w", err)
	}
	return nil
}

func (s *sqliteClient) DeleteExpiredBanlistObservations(ctx context.Context, now time.Time, limit int) (int64, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM banlist_observations
		WHERE (user_id, source) IN (
			SELECT user_id, source
			FROM banlist_observations
			WHERE expires_at <= ?
			ORDER BY expires_at
			LIMIT ?
		)
	`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired banlist observations: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read expired banlist observation count: %w", err)
	}
	return deleted, nil
}
