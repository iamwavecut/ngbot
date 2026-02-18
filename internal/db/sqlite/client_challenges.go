package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func (c *sqliteClient) CreateChallenge(ctx context.Context, challenge *db.Challenge) (*db.Challenge, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		INSERT INTO gatekeeper_challenges (
			comm_chat_id, user_id, chat_id, success_uuid, join_message_id, challenge_message_id, attempts, created_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(comm_chat_id, user_id) DO UPDATE SET
			chat_id = excluded.chat_id,
			success_uuid = excluded.success_uuid,
			join_message_id = excluded.join_message_id,
			challenge_message_id = excluded.challenge_message_id,
			attempts = excluded.attempts,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at
	`
	_, err := c.db.ExecContext(ctx, query,
		challenge.CommChatID,
		challenge.UserID,
		challenge.ChatID,
		challenge.SuccessUUID,
		challenge.JoinMessageID,
		challenge.ChallengeMessageID,
		challenge.Attempts,
		challenge.CreatedAt,
		challenge.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return challenge, nil
}

func (c *sqliteClient) GetChallenge(ctx context.Context, commChatID, userID int64) (*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenge db.Challenge
	err := c.db.GetContext(ctx, &challenge, `
		SELECT comm_chat_id, user_id, chat_id, success_uuid, join_message_id, challenge_message_id, attempts, created_at, expires_at
		FROM gatekeeper_challenges
		WHERE comm_chat_id = ? AND user_id = ?
	`, commChatID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &challenge, nil
}

func (c *sqliteClient) UpdateChallenge(ctx context.Context, challenge *db.Challenge) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	query := `
		UPDATE gatekeeper_challenges
		SET chat_id = ?,
			success_uuid = ?,
			join_message_id = ?,
			challenge_message_id = ?,
			attempts = ?,
			created_at = ?,
			expires_at = ?
		WHERE comm_chat_id = ? AND user_id = ?
	`
	_, err := c.db.ExecContext(ctx, query,
		challenge.ChatID,
		challenge.SuccessUUID,
		challenge.JoinMessageID,
		challenge.ChallengeMessageID,
		challenge.Attempts,
		challenge.CreatedAt,
		challenge.ExpiresAt,
		challenge.CommChatID,
		challenge.UserID,
	)
	return err
}

func (c *sqliteClient) DeleteChallenge(ctx context.Context, commChatID, userID int64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, err := c.db.ExecContext(ctx, `DELETE FROM gatekeeper_challenges WHERE comm_chat_id = ? AND user_id = ?`, commChatID, userID)
	return err
}

func (c *sqliteClient) GetExpiredChallenges(ctx context.Context, now time.Time) ([]*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenges []*db.Challenge
	err := c.db.SelectContext(ctx, &challenges, `
		SELECT comm_chat_id, user_id, chat_id, success_uuid, join_message_id, challenge_message_id, attempts, created_at, expires_at
		FROM gatekeeper_challenges
		WHERE expires_at <= ?
	`, now)
	return challenges, err
}
