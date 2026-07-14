package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const spamCaseColumns = `
	id, chat_id, user_id, message_id, message_text, created_at,
	channel_username, channel_post_id, notification_message_id,
	pre_vote_restricted, status, resolved_at, resolve_at,
	next_attempt_at, attempt_count, last_error
`

func (s *sqliteClient) AddRestriction(ctx context.Context, restriction *db.UserRestriction) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO user_restrictions (user_id, chat_id, restricted_at, expires_at, reason)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, user_id) DO UPDATE SET
			restricted_at = excluded.restricted_at,
			expires_at = excluded.expires_at,
			reason = excluded.reason
	`
	_, err := s.db.ExecContext(
		ctx, query,
		restriction.UserID,
		restriction.ChatID,
		restriction.RestrictedAt,
		restriction.ExpiresAt,
		restriction.Reason,
	)
	return err
}

func (s *sqliteClient) RemoveRestriction(ctx context.Context, chatID int64, userID int64) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `DELETE FROM user_restrictions WHERE chat_id = ? AND user_id = ?`
	_, err := s.db.ExecContext(ctx, query, chatID, userID)
	return err
}

func (s *sqliteClient) CreateSpamCase(ctx context.Context, sc *db.SpamCase) (*db.SpamCase, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO spam_cases (chat_id, user_id, message_id, message_text, created_at, channel_username, channel_post_id,
			notification_message_id, pre_vote_restricted, status, resolved_at, resolve_at, next_attempt_at, attempt_count, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.ExecContext(
		ctx, query,
		sc.ChatID,
		sc.UserID,
		sc.MessageID,
		sc.MessageText,
		sc.CreatedAt,
		sc.ChannelUsername,
		sc.ChannelPostID,
		sc.NotificationMessageID,
		sc.PreVoteRestricted,
		sc.Status,
		sc.ResolvedAt,
		sc.ResolveAt,
		sc.NextAttemptAt,
		sc.AttemptCount,
		sc.LastError,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	sc.ID = id
	return sc, nil
}

func (s *sqliteClient) UpdateSpamCase(ctx context.Context, sc *db.SpamCase) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		UPDATE spam_cases
		SET channel_username = ?,
			channel_post_id = ?,
			notification_message_id = ?,
			message_id = ?,
			pre_vote_restricted = ?,
			status = ?,
			resolved_at = ?,
			resolve_at = ?,
			next_attempt_at = ?,
			attempt_count = ?,
			last_error = ?
		WHERE id = ?
	`
	_, err := s.db.ExecContext(
		ctx, query,
		sc.ChannelUsername,
		sc.ChannelPostID,
		sc.NotificationMessageID,
		sc.MessageID,
		sc.PreVoteRestricted,
		sc.Status,
		sc.ResolvedAt,
		sc.ResolveAt,
		sc.NextAttemptAt,
		sc.AttemptCount,
		sc.LastError,
		sc.ID,
	)
	return err
}

func (s *sqliteClient) UpdateSpamCasePresentation(ctx context.Context, sc *db.SpamCase) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	result, err := s.db.ExecContext(ctx, `
		UPDATE spam_cases
		SET channel_username = ?, channel_post_id = ?, notification_message_id = ?
		WHERE id = ? AND status = ?
	`, sc.ChannelUsername, sc.ChannelPostID, sc.NotificationMessageID, sc.ID, db.SpamCaseStatusPending)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("spam case is no longer pending")
	}
	return nil
}

func (s *sqliteClient) SetSpamCasePreVoteRestricted(ctx context.Context, caseID int64, restricted bool) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	result, err := s.db.ExecContext(ctx, `
		UPDATE spam_cases
		SET pre_vote_restricted = ?
		WHERE id = ? AND status = ?
	`, restricted, caseID, db.SpamCaseStatusPending)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("spam case is no longer pending")
	}
	return nil
}

func (s *sqliteClient) SetSpamCaseResolveAt(ctx context.Context, caseID int64, resolveAt time.Time) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	result, err := s.db.ExecContext(ctx, `
		UPDATE spam_cases
		SET resolve_at = ?
		WHERE id = ? AND status = ? AND resolve_at IS NULL
	`, resolveAt, caseID, db.SpamCaseStatusPending)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *sqliteClient) GetSpamCase(ctx context.Context, id int64) (*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var sc db.SpamCase
	err := s.db.GetContext(ctx, &sc, `SELECT `+spamCaseColumns+` FROM spam_cases WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

func (s *sqliteClient) GetActiveSpamCase(ctx context.Context, chatID, userID int64) (*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var sc db.SpamCase
	err := s.db.GetContext(ctx, &sc, `
		SELECT `+spamCaseColumns+` FROM spam_cases
		WHERE chat_id = ?
		AND user_id = ?
		AND status = 'pending'
		AND resolved_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`, chatID, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sc, nil
}

func (s *sqliteClient) GetActiveSpamCaseByMessage(ctx context.Context, chatID, userID int64, messageID int) (*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var sc db.SpamCase
	err := s.db.GetContext(ctx, &sc, `
		SELECT `+spamCaseColumns+` FROM spam_cases
		WHERE chat_id = ?
		AND user_id = ?
		AND message_id = ?
		AND status = 'pending'
		AND resolved_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`, chatID, userID, messageID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sc, nil
}

func (s *sqliteClient) GetPendingSpamCases(ctx context.Context) ([]*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var cases []*db.SpamCase
	err := s.db.SelectContext(ctx, &cases, `
		SELECT `+spamCaseColumns+` FROM spam_cases
		WHERE status = 'pending' AND resolved_at IS NULL
		ORDER BY created_at DESC
	`)
	return cases, err
}

func (s *sqliteClient) GetDueSpamCases(ctx context.Context, now time.Time) ([]*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var cases []*db.SpamCase
	err := s.db.SelectContext(
		ctx, &cases, `
		SELECT `+spamCaseColumns+` FROM spam_cases
		WHERE (status = ? AND resolve_at IS NOT NULL AND resolve_at <= ?)
			OR (status IN (?, ?) AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?)
		ORDER BY COALESCE(next_attempt_at, resolve_at, created_at), id
	`,
		db.SpamCaseStatusPending,
		now,
		db.SpamCaseStatusResolvingSpam,
		db.SpamCaseStatusResolvingFalsePositive,
		now,
	)
	return cases, err
}

func (s *sqliteClient) AddSpamCaseReportMessage(ctx context.Context, message *db.SpamCaseReportMessage) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT OR IGNORE INTO spam_case_report_messages (case_id, chat_id, message_id, created_at)
		VALUES (?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(
		ctx, query,
		message.CaseID,
		message.ChatID,
		message.MessageID,
		message.CreatedAt,
	)
	return err
}

func (s *sqliteClient) GetSpamCaseReportMessages(ctx context.Context, caseID int64) ([]*db.SpamCaseReportMessage, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var messages []*db.SpamCaseReportMessage
	err := s.db.SelectContext(ctx, &messages, `
		SELECT * FROM spam_case_report_messages
		WHERE case_id = ?
		ORDER BY created_at ASC, message_id ASC
	`, caseID)
	return messages, err
}

func (s *sqliteClient) DeleteSpamCaseReportMessages(ctx context.Context, caseID int64) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM spam_case_report_messages WHERE case_id = ?`, caseID)
	return err
}

func (s *sqliteClient) GetDueSpamCaseReportMessages(ctx context.Context, before time.Time) ([]*db.SpamCaseReportMessage, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var messages []*db.SpamCaseReportMessage
	err := s.db.SelectContext(ctx, &messages, `
		SELECT case_id, chat_id, message_id, created_at
		FROM spam_case_report_messages
		WHERE created_at <= ?
		ORDER BY created_at, case_id, chat_id, message_id
	`, before)
	return messages, err
}

func (s *sqliteClient) DeleteSpamCaseReportMessage(ctx context.Context, caseID, chatID int64, messageID int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	_, err := s.db.ExecContext(ctx, `
		DELETE FROM spam_case_report_messages
		WHERE case_id = ? AND chat_id = ? AND message_id = ?
	`, caseID, chatID, messageID)
	return err
}

func (s *sqliteClient) AddSpamVote(ctx context.Context, vote *db.SpamVote) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO spam_votes (case_id, voter_id, vote, voted_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(case_id, voter_id) DO UPDATE SET
		vote = excluded.vote,
		voted_at = excluded.voted_at
	`
	_, err := s.db.ExecContext(
		ctx, query,
		vote.CaseID,
		vote.VoterID,
		vote.Vote,
		vote.VotedAt,
	)
	return err
}

func (s *sqliteClient) AddVoteIfPending(ctx context.Context, vote *db.SpamVote) (int, int, bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var pending int
	if err := tx.GetContext(ctx, &pending, `
		SELECT COUNT(*) FROM spam_cases WHERE id = ? AND status = ?
	`, vote.CaseID, db.SpamCaseStatusPending); err != nil {
		return 0, 0, false, err
	}
	if pending != 1 {
		return 0, 0, false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO spam_votes (case_id, voter_id, vote, voted_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(case_id, voter_id) DO UPDATE SET
			vote = excluded.vote,
			voted_at = excluded.voted_at
	`, vote.CaseID, vote.VoterID, vote.Vote, vote.VotedAt); err != nil {
		return 0, 0, false, err
	}

	var counts struct {
		NotSpam int `db:"not_spam"`
		Spam    int `db:"spam"`
	}
	if err := tx.GetContext(ctx, &counts, `
		SELECT
			COALESCE(SUM(CASE WHEN vote THEN 1 ELSE 0 END), 0) AS not_spam,
			COALESCE(SUM(CASE WHEN vote THEN 0 ELSE 1 END), 0) AS spam
		FROM spam_votes WHERE case_id = ?
	`, vote.CaseID); err != nil {
		return 0, 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, false, err
	}
	return counts.NotSpam, counts.Spam, true, nil
}

func (s *sqliteClient) ClaimKnownSpamCase(ctx context.Context, caseID int64, now time.Time) (*db.SpamCase, bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var spamCase db.SpamCase
	if err := tx.GetContext(ctx, &spamCase, `
		SELECT `+spamCaseColumns+` FROM spam_cases
		WHERE id = ?
			AND status = ?
			AND resolved_at IS NULL
			AND resolve_at IS NULL
			AND pre_vote_restricted = FALSE
			AND message_id != 0
	`, caseID, db.SpamCaseStatusPending); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE spam_cases
		SET status = ?, next_attempt_at = ?, attempt_count = 0, last_error = ''
		WHERE id = ?
			AND status = ?
			AND resolved_at IS NULL
			AND resolve_at IS NULL
			AND pre_vote_restricted = FALSE
			AND message_id != 0
	`, db.SpamCaseStatusResolvingSpam, now, caseID, db.SpamCaseStatusPending)
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected != 1 {
		return nil, false, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	spamCase.Status = db.SpamCaseStatusResolvingSpam
	spamCase.NextAttemptAt = sql.NullTime{Time: now, Valid: true}
	spamCase.AttemptCount = 0
	spamCase.LastError = ""
	return &spamCase, true, nil
}

func (s *sqliteClient) ClaimSpamCaseResolution(
	ctx context.Context,
	caseID int64,
	requiredVoters int,
	timedOut bool,
	now time.Time,
) (*db.SpamCase, bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var spamCase db.SpamCase
	if err := tx.GetContext(ctx, &spamCase, `
		SELECT `+spamCaseColumns+` FROM spam_cases
		WHERE id = ? AND status = ?
	`, caseID, db.SpamCaseStatusPending); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var counts struct {
		NotSpam int `db:"not_spam"`
		Spam    int `db:"spam"`
	}
	if err := tx.GetContext(ctx, &counts, `
		SELECT
			COALESCE(SUM(CASE WHEN vote THEN 1 ELSE 0 END), 0) AS not_spam,
			COALESCE(SUM(CASE WHEN vote THEN 0 ELSE 1 END), 0) AS spam
		FROM spam_votes WHERE case_id = ?
	`, caseID); err != nil {
		return nil, false, err
	}
	total := counts.NotSpam + counts.Spam
	if total < requiredVoters && !timedOut {
		return nil, false, nil
	}
	if counts.NotSpam == counts.Spam && !timedOut {
		return nil, false, nil
	}
	nextStatus := db.SpamCaseStatusResolvingFalsePositive
	if counts.Spam > counts.NotSpam && total >= requiredVoters {
		nextStatus = db.SpamCaseStatusResolvingSpam
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE spam_cases
		SET status = ?, next_attempt_at = ?, attempt_count = 0, last_error = ''
		WHERE id = ? AND status = ?
	`, nextStatus, now, caseID, db.SpamCaseStatusPending)
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected != 1 {
		return nil, false, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	spamCase.Status = nextStatus
	spamCase.NextAttemptAt = sql.NullTime{Time: now, Valid: true}
	return &spamCase, true, nil
}

func (s *sqliteClient) FinalizeSpamCaseResolution(
	ctx context.Context,
	caseID int64,
	expectedStatus, terminalStatus, statsKey string,
	resolvedAt time.Time,
) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE spam_cases
		SET status = ?, resolved_at = ?, next_attempt_at = NULL, last_error = ''
		WHERE id = ? AND status = ?
	`, terminalStatus, resolvedAt, caseID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	if statsKey != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO kv_store (key, value, updated_at)
			VALUES (?, '1', datetime('now'))
			ON CONFLICT(key) DO UPDATE SET
				value = CAST(CAST(kv_store.value AS INTEGER) + 1 AS TEXT),
				updated_at = excluded.updated_at
		`, statsKey); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *sqliteClient) ScheduleSpamCaseRetry(
	ctx context.Context,
	caseID int64,
	expectedStatus string,
	nextAttemptAt time.Time,
	lastError string,
) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	result, err := s.db.ExecContext(ctx, `
		UPDATE spam_cases
		SET next_attempt_at = ?, attempt_count = attempt_count + 1, last_error = ?
		WHERE id = ? AND status = ?
	`, nullableTime(nextAttemptAt), lastError, caseID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *sqliteClient) GetSpamVotes(ctx context.Context, caseID int64) ([]*db.SpamVote, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var votes []*db.SpamVote
	err := s.db.SelectContext(ctx, &votes, `
		SELECT * FROM spam_votes
		WHERE case_id = ?
		ORDER BY voted_at ASC
	`, caseID)
	return votes, err
}

func (s *sqliteClient) GetActiveRestriction(ctx context.Context, chatID, userID int64) (*db.UserRestriction, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var restriction db.UserRestriction
	err := s.db.GetContext(ctx, &restriction, `
		SELECT * FROM user_restrictions
		WHERE chat_id = ? AND user_id = ? AND expires_at > datetime('now')
		ORDER BY restricted_at DESC LIMIT 1
	`, chatID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &restriction, nil
}

func (s *sqliteClient) RemoveExpiredRestrictions(ctx context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `DELETE FROM user_restrictions WHERE expires_at <= datetime('now')`
	_, err := s.db.ExecContext(ctx, query)
	return err
}

func (s *sqliteClient) AddChatRecentJoiner(ctx context.Context, joiner *db.RecentJoiner) (*db.RecentJoiner, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO recent_joiners (chat_id, user_id, username, joined_at, join_message_id, processed, is_spammer)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_id, user_id) DO UPDATE SET
			username = excluded.username,
			joined_at = excluded.joined_at,
			join_message_id = CASE
				WHEN excluded.join_message_id != 0 THEN excluded.join_message_id
				ELSE recent_joiners.join_message_id
			END,
			processed = FALSE,
			is_spammer = FALSE
	`
	if _, err := s.db.ExecContext(
		ctx, query,
		joiner.ChatID,
		joiner.UserID,
		joiner.Username,
		joiner.JoinedAt,
		joiner.JoinMessageID,
		joiner.Processed,
		joiner.IsSpammer,
	); err != nil {
		return nil, err
	}

	var stored db.RecentJoiner
	if err := s.db.GetContext(ctx, &stored, `
		SELECT * FROM recent_joiners
		WHERE chat_id = ? AND user_id = ?
		LIMIT 1
	`, joiner.ChatID, joiner.UserID); err != nil {
		return nil, err
	}
	*joiner = stored
	return joiner, nil
}

func (s *sqliteClient) GetChatRecentJoiners(ctx context.Context, chatID int64) ([]*db.RecentJoiner, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var joiners []*db.RecentJoiner
	err := s.db.SelectContext(ctx, &joiners, `
		SELECT * FROM recent_joiners
		WHERE chat_id = ?
		ORDER BY joined_at DESC
	`, chatID)
	return joiners, err
}

func (s *sqliteClient) GetUnprocessedRecentJoiners(ctx context.Context) ([]*db.RecentJoiner, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var joiners []*db.RecentJoiner
	err := s.db.SelectContext(ctx, &joiners, `
		SELECT * FROM recent_joiners
		WHERE processed = FALSE
		ORDER BY joined_at ASC
	`)
	return joiners, err
}

func (s *sqliteClient) ProcessRecentJoiner(ctx context.Context, chatID int64, userID int64, isSpammer bool) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		UPDATE recent_joiners
		SET processed = TRUE, is_spammer = ?
		WHERE chat_id = ? AND user_id = ?
	`
	_, err := s.db.ExecContext(ctx, query, isSpammer, chatID, userID)
	return err
}

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
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin banlist source transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replace {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM banlist_sources WHERE provider = ? AND feed_type = ?
		`, provider, feedType); err != nil {
			return fmt.Errorf("replace banlist source %s/%s: %w", provider, feedType, err)
		}
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO banlist_sources (user_id, provider, feed_type, generation, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, provider, feed_type) DO UPDATE SET
			generation = excluded.generation,
			last_seen_at = excluded.last_seen_at,
			expires_at = excluded.expires_at
	`)
	if err != nil {
		return fmt.Errorf("prepare banlist source insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, userID := range userIDs {
		if _, err := stmt.ExecContext(ctx, userID, provider, feedType, generation, seenAt, expiresAt); err != nil {
			return fmt.Errorf("insert banlist source user %d: %w", userID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM banlist_sources WHERE expires_at IS NOT NULL AND expires_at <= ?
	`, seenAt); err != nil {
		return fmt.Errorf("expire banlist sources: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM banlist`); err != nil {
		return fmt.Errorf("clear effective banlist: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO banlist (user_id)
		SELECT DISTINCT user_id
		FROM banlist_sources
		WHERE expires_at IS NULL OR expires_at > ?
	`, seenAt); err != nil {
		return fmt.Errorf("rebuild effective banlist: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit banlist source transaction: %w", err)
	}
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
	results := make(map[int64]struct{})
	for _, userID := range userIDs {
		results[userID] = struct{}{}
	}
	return results, nil
}
