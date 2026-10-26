package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const (
	banlistResultNotModified = "not_modified"
	banlistResultUnchanged   = "unchanged"
	banlistResultChanged     = "changed"
	banlistResultHeldShrink  = "held_shrink"
	banlistResultFailed      = "failed"
)

var errBanlistSnapshotMissing = errors.New("banlist snapshot row is missing")

type (
	banlistSourceState struct {
		exists       bool
		frozen       bool
		corrupt      bool
		count        int
		sha256       []byte
		etag         sql.NullString
		lastModified sql.NullString
		checkedAt    time.Time
	}

	pendingBanlistShrink struct {
		sha256    [sha256.Size]byte
		firstSeen time.Time
	}

	banlistRefreshOutcome struct {
		result       string
		count        int
		added        int
		removed      int
		payloadBytes int
	}
)

func (state banlistSourceState) downloaded() bool {
	return state.checkedAt.Unix() > 0
}

func (state banlistSourceState) due(source banlistSourceSpec, now time.Time) bool {
	if state.frozen {
		return false
	}
	if source.observationTTL == 0 && (!state.exists || state.corrupt) {
		return true
	}
	return now.Sub(state.checkedAt) >= source.interval
}

func (s *defaultBanService) loadBanlists(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	s.refreshMutex.Lock()
	defer s.refreshMutex.Unlock()

	snapshots, err := s.db.BanlistSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("load banlist snapshots: %w", err)
	}
	states := make(map[string]banlistSourceState, len(snapshots))
	index := &banlistIndex{sources: make(map[string][]int64, len(snapshots))}
	for i := range snapshots {
		snapshot := &snapshots[i]
		state := banlistSourceState{
			exists:       true,
			frozen:       snapshot.Frozen,
			count:        snapshot.IDCount,
			sha256:       snapshot.PayloadSHA256,
			etag:         snapshot.ETag,
			lastModified: snapshot.LastModified,
			checkedAt:    snapshot.CheckedAt,
		}
		ids, err := decodeBanlistSnapshot(snapshot)
		snapshot.Payload = nil
		if err != nil {
			state.corrupt = true
			log.WithFields(log.Fields{
				logFieldSource: snapshot.Source,
				logFieldError:  err.Error(),
			}).Error("ignoring corrupt banlist snapshot")
		} else {
			index.sources[snapshot.Source] = ids
		}
		states[snapshot.Source] = state
		log.WithFields(log.Fields{
			logFieldSource: snapshot.Source,
			"count":        snapshot.IDCount,
			"frozen":       snapshot.Frozen,
			"checked_at":   snapshot.CheckedAt,
		}).Info("loaded banlist snapshot")
	}

	now := s.currentTime()
	observations, err := s.db.BanlistObservations(ctx, now)
	if err != nil {
		return fmt.Errorf("load banlist observations: %w", err)
	}
	expiries := make(map[int64]time.Time, len(observations))
	for _, observation := range observations {
		if current, ok := expiries[observation.UserID]; !ok || observation.ExpiresAt.After(current) {
			expiries[observation.UserID] = observation.ExpiresAt
		}
		state := states[observation.Source]
		if observation.ObservedAt.After(state.checkedAt) {
			state.checkedAt = observation.ObservedAt
			states[observation.Source] = state
		}
	}
	log.WithField("count", len(observations)).Info("loaded banlist observations")

	s.mapMutex.Lock()
	s.index = index
	s.observations = expiries
	s.mapMutex.Unlock()
	s.sourceStates = states
	s.pendingShrinks = make(map[string]pendingBanlistShrink)
	return nil
}

func decodeBanlistSnapshot(snapshot *db.BanlistSnapshot) ([]int64, error) {
	if snapshot.Format != db.BanlistSnapshotFormatV1 {
		return nil, fmt.Errorf("unsupported format %d: %w", snapshot.Format, db.ErrCorruptBanlistPayload)
	}
	sum := sha256.Sum256(snapshot.Payload)
	if !bytes.Equal(sum[:], snapshot.PayloadSHA256) {
		return nil, fmt.Errorf("payload hash mismatch: %w", db.ErrCorruptBanlistPayload)
	}
	return db.DecodeBanlistIDs(snapshot.Payload, snapshot.IDCount)
}

func (s *defaultBanService) refreshDueSources(ctx context.Context, now time.Time) {
	if s.db == nil {
		return
	}
	s.refreshMutex.Lock()
	defer s.refreshMutex.Unlock()
	if s.sourceStates == nil {
		s.sourceStates = make(map[string]banlistSourceState)
	}
	if s.pendingShrinks == nil {
		s.pendingShrinks = make(map[string]pendingBanlistShrink)
	}

	for _, source := range s.sources {
		if ctx.Err() != nil {
			return
		}
		if s.sourceStates[source.name].due(source, now) {
			s.refreshSource(ctx, source, now)
		}
	}
}

func (s *defaultBanService) refreshSource(ctx context.Context, source banlistSourceSpec, now time.Time) {
	state := s.sourceStates[source.name]
	var etag, lastModified string
	if source.observationTTL == 0 && !state.corrupt {
		etag, lastModified = state.etag.String, state.lastModified.String
	}

	fetchStartedAt := time.Now()
	fetch, err := fetchBanlistSource(ctx, s.downloadClient, source.url, etag, lastModified, state.count)
	applyStartedAt := time.Now()
	var outcome banlistRefreshOutcome
	if err == nil {
		if source.observationTTL > 0 {
			outcome, err = s.applyObservationFetch(ctx, source, fetch, now)
		} else {
			outcome, err = s.applySnapshotFetch(ctx, source, state, fetch, now)
		}
	}
	fields := log.Fields{
		logFieldSource:   source.name,
		"fetch_duration": applyStartedAt.Sub(fetchStartedAt),
		"apply_duration": time.Since(applyStartedAt),
	}
	if err != nil {
		if errorsIsCanceled(err) && ctx.Err() != nil {
			return
		}
		fields["result"] = banlistResultFailed
		fields[logFieldError] = err.Error()
		log.WithFields(fields).Warn("failed to refresh banlist source")
		return
	}
	fields["result"] = outcome.result
	fields["count"] = outcome.count
	fields["added"] = outcome.added
	fields["removed"] = outcome.removed
	fields["payload_bytes"] = outcome.payloadBytes
	log.WithFields(fields).Debug("refreshed banlist source")
}

func (s *defaultBanService) applySnapshotFetch(
	ctx context.Context,
	source banlistSourceSpec,
	state banlistSourceState,
	fetch banlistFetch,
	now time.Time,
) (banlistRefreshOutcome, error) {
	etag := sql.NullString{String: fetch.ETag, Valid: fetch.ETag != ""}
	lastModified := sql.NullString{String: fetch.LastModified, Valid: fetch.LastModified != ""}
	if fetch.NotModified {
		if err := s.touchSnapshot(ctx, source.name, etag, lastModified, now); err != nil {
			return banlistRefreshOutcome{}, err
		}
		return banlistRefreshOutcome{result: banlistResultNotModified, count: state.count}, nil
	}

	payload, err := db.EncodeBanlistIDs(fetch.IDs)
	if err != nil {
		return banlistRefreshOutcome{}, fmt.Errorf("encode %s: %w", source.name, err)
	}
	sum := sha256.Sum256(payload)
	outcome := banlistRefreshOutcome{count: len(fetch.IDs), payloadBytes: len(payload)}
	if state.exists && !state.corrupt && bytes.Equal(sum[:], state.sha256) {
		delete(s.pendingShrinks, source.name)
		if err := s.touchSnapshot(ctx, source.name, etag, lastModified, now); err != nil {
			return banlistRefreshOutcome{}, err
		}
		outcome.result = banlistResultUnchanged
		return outcome, nil
	}
	if s.holdShrink(source.name, state, len(fetch.IDs), sum, now) {
		outcome.result = banlistResultHeldShrink
		return outcome, nil
	}

	if err := s.db.SaveBanlistSnapshot(ctx, &db.BanlistSnapshot{
		Source:        source.name,
		Format:        db.BanlistSnapshotFormatV1,
		IDCount:       len(fetch.IDs),
		Payload:       payload,
		PayloadSHA256: sum[:],
		ETag:          etag,
		LastModified:  lastModified,
		ChangedAt:     now,
		CheckedAt:     now,
	}); err != nil {
		return banlistRefreshOutcome{}, fmt.Errorf("save %s: %w", source.name, err)
	}
	s.mapMutex.RLock()
	previous := s.index.ids(source.name)
	s.mapMutex.RUnlock()
	outcome.added, outcome.removed = diffSortedIDs(previous, fetch.IDs)
	s.publishSource(source.name, fetch.IDs)
	s.sourceStates[source.name] = banlistSourceState{
		exists:       true,
		count:        len(fetch.IDs),
		sha256:       sum[:],
		etag:         etag,
		lastModified: lastModified,
		checkedAt:    now,
	}
	delete(s.pendingShrinks, source.name)
	outcome.result = banlistResultChanged
	return outcome, nil
}

func (s *defaultBanService) touchSnapshot(ctx context.Context, source string, etag, lastModified sql.NullString, now time.Time) error {
	touched, err := s.db.TouchBanlistSnapshot(ctx, source, etag, lastModified, now)
	if err != nil {
		return fmt.Errorf("touch %s: %w", source, err)
	}
	state := s.sourceStates[source]
	if !touched {
		state.exists = false
		s.sourceStates[source] = state
		return fmt.Errorf("touch %s: %w", source, errBanlistSnapshotMissing)
	}
	state.etag = etag
	state.lastModified = lastModified
	state.checkedAt = now
	s.sourceStates[source] = state
	return nil
}

func (s *defaultBanService) holdShrink(source string, state banlistSourceState, count int, sum [sha256.Size]byte, now time.Time) bool {
	if !state.exists || state.corrupt || (count > 0 && !state.downloaded()) || float64(count) >= banlistShrinkThreshold*float64(state.count) {
		delete(s.pendingShrinks, source)
		return false
	}
	pending, seen := s.pendingShrinks[source]
	if count > 0 && seen && pending.sha256 == sum && now.Sub(pending.firstSeen) >= banlistShrinkConfirmDelay {
		delete(s.pendingShrinks, source)
		return false
	}
	if !seen || pending.sha256 != sum {
		pending = pendingBanlistShrink{sha256: sum, firstSeen: now}
		s.pendingShrinks[source] = pending
	}
	log.WithFields(log.Fields{
		logFieldSource:   source,
		"stored_count":   state.count,
		"incoming_count": count,
		"first_seen":     pending.firstSeen,
	}).Error("holding shrunken banlist until the same payload is confirmed")
	return true
}

func (s *defaultBanService) applyObservationFetch(
	ctx context.Context,
	source banlistSourceSpec,
	fetch banlistFetch,
	now time.Time,
) (banlistRefreshOutcome, error) {
	expiresAt := now.Add(source.observationTTL)
	observations := make([]db.BanlistObservation, 0, len(fetch.IDs))
	for _, userID := range fetch.IDs {
		observations = append(observations, db.BanlistObservation{
			UserID:     userID,
			Source:     source.name,
			ObservedAt: now,
			ExpiresAt:  expiresAt,
		})
	}
	if err := s.db.UpsertBanlistObservations(ctx, observations); err != nil {
		return banlistRefreshOutcome{}, fmt.Errorf("observe %s: %w", source.name, err)
	}
	s.observeAll(fetch.IDs, expiresAt)
	state := s.sourceStates[source.name]
	state.checkedAt = now
	s.sourceStates[source.name] = state
	return banlistRefreshOutcome{result: banlistResultChanged, count: len(fetch.IDs), added: len(fetch.IDs)}, nil
}

func (s *defaultBanService) pruneObservations(ctx context.Context, now time.Time) {
	if s.db != nil {
		for {
			deleted, err := s.db.DeleteExpiredBanlistObservations(ctx, now, banlistObservationPruneBatch)
			if err != nil {
				if !errorsIsCanceled(err) {
					log.WithField(logFieldError, err.Error()).Warn("failed to prune expired banlist observations")
				}
				break
			}
			if deleted < banlistObservationPruneBatch {
				break
			}
		}
	}

	s.mapMutex.Lock()
	defer s.mapMutex.Unlock()
	for userID, expiresAt := range s.observations {
		if !now.Before(expiresAt) {
			delete(s.observations, userID)
		}
	}
}

func diffSortedIDs(before, after []int64) (added, removed int) {
	i, j := 0, 0
	for i < len(before) && j < len(after) {
		switch {
		case before[i] == after[j]:
			i++
			j++
		case before[i] < after[j]:
			removed++
			i++
		default:
			added++
			j++
		}
	}
	return added + len(after) - j, removed + len(before) - i
}
