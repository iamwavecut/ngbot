package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

type legacyBanlistConversion struct {
	provider string
	feedType string
	target   string
	frozen   bool
}

func (s *sqliteClient) convertLegacyBanlist(ctx context.Context) error {
	var legacyTables int
	if err := s.db.GetContext(ctx, &legacyTables, `
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name IN ('banlist_generations', 'banlist_entries')
	`); err != nil {
		return fmt.Errorf("inspect legacy banlist tables: %w", err)
	}
	if legacyTables != 2 {
		return nil
	}
	for _, conversion := range []legacyBanlistConversion{
		{provider: "cas", feedType: "daily", target: db.BanlistSourceCASLegacy, frozen: true},
		{provider: "lols", feedType: "daily", target: db.BanlistSourceLoLsBanlist},
	} {
		if err := s.convertLegacyBanlistSource(ctx, conversion); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteClient) convertLegacyBanlistSource(ctx context.Context, conversion legacyBanlistConversion) error {
	var exists bool
	if err := s.db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM banlist_snapshots WHERE source = ?)`, conversion.target); err != nil {
		return fmt.Errorf("inspect banlist snapshot %s: %w", conversion.target, err)
	}
	if exists {
		return nil
	}

	var generation struct {
		ID         int64     `db:"id"`
		LastSeenAt time.Time `db:"last_seen_at"`
	}
	err := s.db.GetContext(ctx, &generation, `
		SELECT id, last_seen_at
		FROM banlist_generations
		WHERE provider = ? AND feed_type = ? AND active = 1
		ORDER BY last_seen_at DESC, id DESC
		LIMIT 1
	`, conversion.provider, conversion.feedType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select legacy banlist %s/%s: %w", conversion.provider, conversion.feedType, err)
	}

	var ids []int64
	if err := s.db.SelectContext(ctx, &ids, `SELECT user_id FROM banlist_entries WHERE generation_id = ? ORDER BY user_id`, generation.ID); err != nil {
		return fmt.Errorf("read legacy banlist %s/%s: %w", conversion.provider, conversion.feedType, err)
	}
	payload, err := db.EncodeBanlistIDs(ids)
	if err != nil {
		return fmt.Errorf("encode legacy banlist %s/%s: %w", conversion.provider, conversion.feedType, err)
	}
	checkedAt := time.Unix(0, 0).UTC()
	if conversion.frozen {
		checkedAt = generation.LastSeenAt
	}
	sum := sha256.Sum256(payload)
	if err := s.SaveBanlistSnapshot(ctx, &db.BanlistSnapshot{
		Source:        conversion.target,
		Format:        db.BanlistSnapshotFormatV1,
		IDCount:       len(ids),
		Payload:       payload,
		PayloadSHA256: sum[:],
		Frozen:        conversion.frozen,
		ChangedAt:     generation.LastSeenAt,
		CheckedAt:     checkedAt,
	}); err != nil {
		return err
	}
	log.WithFields(log.Fields{
		"source":   conversion.target,
		"id_count": len(ids),
		"bytes":    len(payload),
	}).Info("converted legacy banlist to snapshot")
	return nil
}
