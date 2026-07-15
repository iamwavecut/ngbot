package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/pborman/uuid"
)

const challengeColumns = `
	challenge_id, comm_chat_id, user_id, chat_id, status, success_uuid, web_app_token, join_request_query_id,
	captcha_prompt, captcha_options_json, join_message_id, challenge_message_id, attempts, created_at, expires_at,
	web_app_opened_at, user_language, next_attempt_at, attempt_count, last_error, notice_message_id, user_restricted
`

func (c *sqliteClient) CreateChallenge(ctx context.Context, challenge *db.Challenge) (*db.Challenge, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if challenge.Status == "" {
		challenge.Status = db.ChallengeStatusPending
	}
	if challenge.ChallengeID == "" {
		challenge.ChallengeID = uuid.New()
	}

	query := `
		INSERT INTO gatekeeper_challenges (
			challenge_id, comm_chat_id, user_id, chat_id, status, success_uuid, web_app_token, join_request_query_id, captcha_prompt,
			captcha_options_json, join_message_id, challenge_message_id, attempts, created_at, expires_at, web_app_opened_at,
			user_language, next_attempt_at, attempt_count, last_error, notice_message_id, user_restricted
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(comm_chat_id, user_id, chat_id) DO UPDATE SET
			challenge_id = excluded.challenge_id,
			status = excluded.status,
			success_uuid = excluded.success_uuid,
			web_app_token = excluded.web_app_token,
			join_request_query_id = excluded.join_request_query_id,
			captcha_prompt = excluded.captcha_prompt,
			captcha_options_json = excluded.captcha_options_json,
			join_message_id = excluded.join_message_id,
			challenge_message_id = excluded.challenge_message_id,
			attempts = excluded.attempts,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at,
			web_app_opened_at = excluded.web_app_opened_at,
			user_language = excluded.user_language,
			next_attempt_at = excluded.next_attempt_at,
			attempt_count = excluded.attempt_count,
			last_error = excluded.last_error,
			notice_message_id = excluded.notice_message_id,
			user_restricted = excluded.user_restricted
	`
	_, err := c.db.ExecContext(
		ctx, query,
		challenge.ChallengeID,
		challenge.CommChatID,
		challenge.UserID,
		challenge.ChatID,
		challenge.Status,
		challenge.SuccessUUID,
		challenge.WebAppToken,
		challenge.JoinRequestQueryID,
		challenge.CaptchaPrompt,
		challenge.CaptchaOptionsJSON,
		challenge.JoinMessageID,
		challenge.ChallengeMessageID,
		challenge.Attempts,
		challenge.CreatedAt,
		challenge.ExpiresAt,
		challenge.WebAppOpenedAt,
		challenge.UserLanguage,
		challenge.NextAttemptAt,
		challenge.AttemptCount,
		challenge.LastError,
		challenge.NoticeMessageID,
		challenge.UserRestricted,
	)
	if err != nil {
		return nil, err
	}
	return challenge, nil
}

func (c *sqliteClient) GetChallengeByMessage(ctx context.Context, commChatID, userID int64, challengeMessageID int) (*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenge db.Challenge
	err := c.db.GetContext(ctx, &challenge, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE comm_chat_id = ? AND user_id = ? AND challenge_message_id = ? AND status = ?
		LIMIT 1
	`, commChatID, userID, challengeMessageID, db.ChallengeStatusPending)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &challenge, nil
}

func (c *sqliteClient) GetChallengeByWebAppToken(ctx context.Context, token string) (*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenge db.Challenge
	err := c.db.GetContext(ctx, &challenge, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE web_app_token = ? AND web_app_token <> ''
		LIMIT 1
	`, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &challenge, nil
}

func (c *sqliteClient) GetChallengeByChatUser(ctx context.Context, chatID, userID int64) (*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenge db.Challenge
	err := c.db.GetContext(ctx, &challenge, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE chat_id = ? AND user_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, chatID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get challenge by chat user: %w", err)
	}
	return &challenge, nil
}

func (c *sqliteClient) GetPassedJoinRequestChallengeByChatUser(ctx context.Context, chatID, userID int64) (*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenge db.Challenge
	err := c.db.GetContext(ctx, &challenge, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE chat_id = ? AND user_id = ? AND comm_chat_id <> chat_id AND status = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, chatID, userID, db.ChallengeStatusPassedWaitingMemberJoin)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get passed join request challenge by chat user: %w", err)
	}
	return &challenge, nil
}

func (c *sqliteClient) RecordWrongAttempt(ctx context.Context, challengeID string, maxAttempts int) (int, string, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var result struct {
		Attempts int    `db:"attempts"`
		Status   string `db:"status"`
	}
	err := c.db.GetContext(ctx, &result, `
		UPDATE gatekeeper_challenges
		SET attempts = attempts + 1,
			status = CASE WHEN attempts + 1 >= ? THEN ? ELSE status END,
			next_attempt_at = CASE WHEN attempts + 1 >= ? THEN CURRENT_TIMESTAMP ELSE next_attempt_at END,
			attempt_count = CASE WHEN attempts + 1 >= ? THEN 0 ELSE attempt_count END,
			last_error = ''
		WHERE challenge_id = ? AND status = ?
		RETURNING attempts, status
	`, maxAttempts, db.ChallengeStatusRejectPending, maxAttempts, maxAttempts, challengeID, db.ChallengeStatusPending)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return result.Attempts, result.Status, true, nil
}

func (c *sqliteClient) ClaimForApproval(ctx context.Context, challengeID string) (bool, error) {
	return c.transitionChallenge(ctx, challengeID, db.ChallengeStatusPending, db.ChallengeStatusApproveQueryPending, time.Now(), time.Time{})
}

func (c *sqliteClient) BeginDMFallback(ctx context.Context, challengeID string) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?, next_attempt_at = ?, attempt_count = 0, last_error = ''
		WHERE challenge_id = ?
			AND status = ?
			AND web_app_token <> ''
			AND join_request_query_id <> ''
			AND web_app_opened_at IS NULL
	`, db.ChallengeStatusWebAppFallbackPending, time.Now(), challengeID, db.ChallengeStatusPending)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) AttachChallengeMessage(ctx context.Context, challengeID, expectedStatus string, messageID int) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET challenge_message_id = ?, last_error = ''
		WHERE challenge_id = ? AND status = ?
	`, messageID, challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) AttachJoinMessage(ctx context.Context, challengeID, expectedStatus string, messageID int) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET join_message_id = ?
		WHERE challenge_id = ? AND status = ? AND join_message_id = 0
	`, messageID, challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) PrepareDMFallback(
	ctx context.Context,
	challengeID, successUUID, userLanguage string,
	expiresAt time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET success_uuid = ?,
			web_app_token = '',
			captcha_prompt = '',
			captcha_options_json = '',
			challenge_message_id = 0,
			attempts = 0,
			expires_at = ?,
			user_language = ?,
			next_attempt_at = CURRENT_TIMESTAMP,
			last_error = ''
		WHERE challenge_id = ? AND status = ?
	`, successUUID, expiresAt, userLanguage, challengeID, db.ChallengeStatusWebAppFallbackPending)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) CompleteExternalAction(
	ctx context.Context,
	challengeID, expectedStatus, nextStatus string,
	expiresAt time.Time,
) (bool, error) {
	var nextAttemptAt time.Time
	switch nextStatus {
	case db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending:
		nextAttemptAt = time.Now()
	}
	return c.transitionChallenge(ctx, challengeID, expectedStatus, nextStatus, nextAttemptAt, expiresAt)
}

func (c *sqliteClient) ScheduleChallengeRetry(
	ctx context.Context,
	challengeID, expectedStatus string,
	nextAttemptAt time.Time,
	lastError string,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET next_attempt_at = ?, attempt_count = attempt_count + 1, last_error = ?
		WHERE challenge_id = ? AND status = ?
	`, nullableTime(nextAttemptAt), lastError, challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) CompleteChallengeWithoutPrivileges(
	ctx context.Context,
	challengeID, expectedStatus string,
	noticeMessageID int,
	expiresAt time.Time,
	lastError string,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?,
			notice_message_id = ?,
			expires_at = ?,
			next_attempt_at = NULL,
			attempt_count = 0,
			last_error = ?
		WHERE challenge_id = ? AND status = ?
	`, db.ChallengeStatusNoPrivilegesNotice, noticeMessageID, expiresAt, lastError, challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) DeleteChallengeInstance(ctx context.Context, challengeID, expectedStatus string) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		DELETE FROM gatekeeper_challenges
		WHERE challenge_id = ? AND status = ?
	`, challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) GetDueChallenges(ctx context.Context, now time.Time) ([]*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenges []*db.Challenge
	err := c.db.SelectContext(
		ctx, &challenges, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE status IN (?, ?, ?, ?, ?)
			AND next_attempt_at IS NOT NULL
			AND next_attempt_at <= ?
		ORDER BY next_attempt_at, created_at
	`,
		db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending,
		now,
	)
	return challenges, err
}

func (c *sqliteClient) transitionChallenge(
	ctx context.Context,
	challengeID, expectedStatus, nextStatus string,
	nextAttemptAt, expiresAt time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var nextAttempt any
	if !nextAttemptAt.IsZero() {
		nextAttempt = nextAttemptAt
	}
	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?,
			next_attempt_at = ?,
			attempt_count = 0,
			last_error = '',
			expires_at = CASE WHEN ? IS NULL THEN expires_at ELSE ? END
		WHERE challenge_id = ? AND status = ?
	`, nextStatus, nextAttempt, nullableTime(expiresAt), nullableTime(expiresAt), challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func (c *sqliteClient) GetExpiredChallenges(ctx context.Context, now time.Time) ([]*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenges []*db.Challenge
	err := c.db.SelectContext(
		ctx, &challenges, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE expires_at <= ?
			AND status NOT IN (?, ?, ?, ?, ?)
	`,
		now,
		db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending,
	)
	return challenges, err
}

func (c *sqliteClient) MarkWebAppChallengeOpened(ctx context.Context, token string, openedAt time.Time) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET web_app_opened_at = ?
		WHERE web_app_token = ? AND web_app_token <> ''
			AND status = ?
			AND web_app_opened_at IS NULL
	`, openedAt, token, db.ChallengeStatusPending)
	return err
}

func (c *sqliteClient) GetUnopenedWebAppChallenges(ctx context.Context, deadline time.Time) ([]*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenges []*db.Challenge
	err := c.db.SelectContext(ctx, &challenges, `
		SELECT `+challengeColumns+`
		FROM gatekeeper_challenges
		WHERE web_app_token <> ''
			AND join_request_query_id <> ''
			AND status = ?
			AND web_app_opened_at IS NULL
			AND created_at <= ?
	`, db.ChallengeStatusPending, deadline)
	return challenges, err
}
