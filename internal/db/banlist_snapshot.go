package db

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	BanlistSnapshotFormatV1 = 1

	BanlistSourceLoLsBanlist  = "lols/banlist"
	BanlistSourceLoLsScammers = "lols/scammers"
	BanlistSourceLoLsHourly   = "lols/hourly"
	BanlistSourceCASLegacy    = "cas/legacy"
)

var ErrCorruptBanlistPayload = errors.New("corrupt banlist payload")

type (
	BanlistSnapshot struct {
		Source        string         `db:"source"`
		Format        int            `db:"format"`
		IDCount       int            `db:"id_count"`
		Payload       []byte         `db:"payload"`
		PayloadSHA256 []byte         `db:"payload_sha256"`
		ETag          sql.NullString `db:"etag"`
		LastModified  sql.NullString `db:"last_modified"`
		Frozen        bool           `db:"frozen"`
		ChangedAt     time.Time      `db:"changed_at"`
		CheckedAt     time.Time      `db:"checked_at"`
	}

	BanlistObservation struct {
		UserID     int64     `db:"user_id"`
		Source     string    `db:"source"`
		ObservedAt time.Time `db:"observed_at"`
		ExpiresAt  time.Time `db:"expires_at"`
	}
)

func NormalizeBanlistIDs(ids []int64) []int64 {
	slices.Sort(ids)
	return slices.Compact(ids)
}

func EncodeBanlistIDs(sorted []int64) ([]byte, error) {
	if len(sorted) == 0 {
		return []byte{}, nil
	}
	payload := make([]byte, 0, len(sorted)*2+binary.MaxVarintLen64)
	payload = binary.AppendVarint(payload, sorted[0])
	for i := 1; i < len(sorted); i++ {
		if sorted[i] <= sorted[i-1] {
			return nil, fmt.Errorf("encode banlist IDs at index %d: %w", i, ErrCorruptBanlistPayload)
		}
		payload = binary.AppendUvarint(payload, uint64(sorted[i])-uint64(sorted[i-1]))
	}
	return payload, nil
}

func DecodeBanlistIDs(payload []byte, count int) ([]int64, error) {
	if count < 0 {
		return nil, fmt.Errorf("decode banlist IDs: negative count %d: %w", count, ErrCorruptBanlistPayload)
	}
	ids := make([]int64, 0, count)
	if count == 0 {
		if len(payload) != 0 {
			return nil, fmt.Errorf("decode banlist IDs: payload without IDs: %w", ErrCorruptBanlistPayload)
		}
		return ids, nil
	}
	first, n := binary.Varint(payload)
	if n <= 0 {
		return nil, fmt.Errorf("decode banlist IDs: first value: %w", ErrCorruptBanlistPayload)
	}
	payload = payload[n:]
	ids = append(ids, first)
	for len(ids) < count {
		delta, n := binary.Uvarint(payload)
		if n <= 0 || delta == 0 {
			return nil, fmt.Errorf("decode banlist IDs at index %d: %w", len(ids), ErrCorruptBanlistPayload)
		}
		payload = payload[n:]
		next := int64(uint64(ids[len(ids)-1]) + delta)
		if next <= ids[len(ids)-1] {
			return nil, fmt.Errorf("decode banlist IDs at index %d: %w", len(ids), ErrCorruptBanlistPayload)
		}
		ids = append(ids, next)
	}
	if len(payload) != 0 {
		return nil, fmt.Errorf("decode banlist IDs: %d trailing bytes: %w", len(payload), ErrCorruptBanlistPayload)
	}
	return ids, nil
}
