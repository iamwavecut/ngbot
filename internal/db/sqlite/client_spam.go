package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

func (s *sqliteClient) AddRestriction(ctx context.Context, restriction *db.UserRestriction) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	query := `
		INSERT INTO user_restrictions (user_id, chat_id, restricted_at, expires_at, reason)
		VALUES (?, ?, ?, ?, ?)
	`
	_, err := s.db.ExecContext(ctx, query,
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
		INSERT INTO spam_cases (chat_id, user_id, message_text, created_at, channel_username, channel_post_id, 
			notification_message_id, status, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.ExecContext(ctx, query,
		sc.ChatID,
		sc.UserID,
		sc.MessageText,
		sc.CreatedAt,
		sc.ChannelUsername,
		sc.ChannelPostID,
		sc.NotificationMessageID,
		sc.Status,
		sc.ResolvedAt,
	)
	if err != nil {
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
			status = ?,
			resolved_at = ?
		WHERE id = ?
	`
	_, err := s.db.ExecContext(ctx, query,
		sc.ChannelUsername,
		sc.ChannelPostID,
		sc.NotificationMessageID,
		sc.Status,
		sc.ResolvedAt,
		sc.ID,
	)
	return err
}

func (s *sqliteClient) GetSpamCase(ctx context.Context, id int64) (*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var sc db.SpamCase
	err := s.db.GetContext(ctx, &sc, `SELECT * FROM spam_cases WHERE id = ?`, id)
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
		SELECT * FROM spam_cases 
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

func (s *sqliteClient) GetPendingSpamCases(ctx context.Context) ([]*db.SpamCase, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var cases []*db.SpamCase
	err := s.db.SelectContext(ctx, &cases, `
		SELECT * FROM spam_cases 
		WHERE status = 'pending' AND resolved_at IS NULL
		ORDER BY created_at DESC
	`)
	return cases, err
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
	_, err := s.db.ExecContext(ctx, query,
		vote.CaseID,
		vote.VoterID,
		vote.Vote,
		vote.VotedAt,
	)
	return err
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
	`
	result, err := s.db.ExecContext(ctx, query,
		joiner.ChatID,
		joiner.UserID,
		joiner.Username,
		joiner.JoinedAt,
		joiner.JoinMessageID,
		joiner.Processed,
		joiner.IsSpammer,
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	joiner.ID = id
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
	defer stmt.Close()

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
