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
	web_app_opened_at, user_language, next_attempt_at, attempt_count, last_error, notice_message_id, user_restricted,
	action_owner, action_lease_until, action_version, action_phase, effect_started_at, cancel_requested
`

var ErrChallengeActionInProgress = errors.New("gatekeeper challenge action is in progress")

func (c *sqliteClient) CreateChallenge(ctx context.Context, challenge *db.Challenge) (*db.Challenge, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if challenge.Status == "" {
		challenge.Status = db.ChallengeStatusPending
	}
	if challenge.ChallengeID == "" {
		challenge.ChallengeID = uuid.New()
	}
	if challenge.ActionPhase == "" {
		challenge.ActionPhase = db.ChallengePhaseReady
	}
	challenge.LastError = db.SafeGatekeeperErrorText(challenge.LastError)

	query := `
		INSERT INTO gatekeeper_challenges (
			challenge_id, comm_chat_id, user_id, chat_id, status, success_uuid, web_app_token, join_request_query_id, captcha_prompt,
			captcha_options_json, join_message_id, challenge_message_id, attempts, created_at, expires_at, web_app_opened_at,
			user_language, next_attempt_at, attempt_count, last_error, notice_message_id, user_restricted, action_owner, action_lease_until,
			action_version, action_phase, effect_started_at, cancel_requested
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			user_restricted = excluded.user_restricted,
			action_owner = excluded.action_owner,
			action_lease_until = excluded.action_lease_until,
			action_version = excluded.action_version,
			action_phase = excluded.action_phase,
			effect_started_at = excluded.effect_started_at,
			cancel_requested = excluded.cancel_requested
		WHERE gatekeeper_challenges.status NOT IN (?, ?, ?, ?, ?, ?, ?)
	`
	result, err := c.db.ExecContext(
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
		challenge.ActionOwner,
		challenge.ActionLeaseUntil,
		challenge.ActionVersion,
		challenge.ActionPhase,
		challenge.EffectStartedAt,
		challenge.CancelRequested,
		db.ChallengeStatusRestrictPending,
		db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending,
		db.ChallengeStatusBanCheckPending,
	)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, ErrChallengeActionInProgress
	}
	return challenge, nil
}

func (c *sqliteClient) ClaimChallengeAction(
	ctx context.Context,
	challengeID, owner string,
	now, leaseUntil time.Time,
) (*db.Challenge, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var challenge db.Challenge
	err := c.db.GetContext(
		ctx, &challenge, `
		UPDATE gatekeeper_challenges
		SET action_owner = ?, action_lease_until = ?, action_version = action_version + 1
		WHERE challenge_id = ?
			AND status IN (?, ?, ?, ?, ?, ?, ?)
			AND next_attempt_at IS NOT NULL
			AND next_attempt_at <= ?
			AND action_phase NOT LIKE '%started'
			AND cancel_requested = FALSE
			AND (action_owner = '' OR action_lease_until IS NULL OR action_lease_until <= ?)
		RETURNING `+challengeColumns+`
	`,
		owner,
		leaseUntil,
		challengeID,
		db.ChallengeStatusRestrictPending,
		db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending,
		db.ChallengeStatusBanCheckPending,
		now,
		now,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &challenge, true, nil
}

func (c *sqliteClient) BeginLeasedChallengeEffect(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, phase string,
	now time.Time,
) (int64, bool, error) {
	return c.advanceLeasedChallengePhase(ctx, challengeID, owner, expectedVersion, expectedStatus, phase, now, true)
}

func (c *sqliteClient) AdvanceLeasedChallengePhase(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, phase string,
	now time.Time,
) (int64, bool, error) {
	return c.advanceLeasedChallengePhase(ctx, challengeID, owner, expectedVersion, expectedStatus, phase, now, false)
}

func (c *sqliteClient) advanceLeasedChallengePhase(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, phase string,
	now time.Time,
	effectStarted bool,
) (int64, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var version int64
	var effectStartedAt any
	if effectStarted {
		effectStartedAt = now
	}
	err := c.db.GetContext(ctx, &version, `
		UPDATE gatekeeper_challenges
		SET action_phase = ?,
			effect_started_at = COALESCE(?, effect_started_at),
			action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ?
			AND action_version = ? AND action_lease_until > ? AND cancel_requested = FALSE
		RETURNING action_version
	`, phase, effectStartedAt, challengeID, expectedStatus, owner, expectedVersion, now)
	if errors.Is(err, sql.ErrNoRows) {
		return expectedVersion, false, nil
	}
	if err != nil {
		return expectedVersion, false, err
	}
	return version, true, nil
}

func (c *sqliteClient) BindLeasedChallengeMessage(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, expectedPhase, completedPhase string,
	messageID int,
	now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET challenge_message_id = ?, action_phase = ?, action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ?
			AND action_version = ? AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
	`, messageID, completedPhase, challengeID, expectedStatus, owner, expectedVersion, expectedPhase, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) ReconcileExpiredChallengeEffect(
	ctx context.Context,
	challengeID, expectedStatus, expectedPhase string,
	artifactMessageID int,
	lastError string,
	now time.Time,
) (bool, error) {
	return c.reconcileChallenge(ctx, challengeID, expectedStatus, expectedPhase, "", 0, artifactMessageID, lastError, now, true)
}

func (c *sqliteClient) ReconcileLeasedChallengeVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus string,
	artifactMessageID int,
	lastError string,
	now time.Time,
) (bool, error) {
	return c.reconcileChallenge(ctx, challengeID, expectedStatus, "", owner, expectedVersion, artifactMessageID, lastError, now, false)
}

func (c *sqliteClient) RequestChallengeCancellation(
	ctx context.Context,
	challengeID, expectedStatus string,
	expectedVersion int64,
	lastError string,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET cancel_requested = TRUE, last_error = ?
		WHERE challenge_id = ? AND status = ? AND action_owner <> ''
			AND action_version = ? AND cancel_requested = FALSE
	`, db.SafeGatekeeperErrorText(lastError), challengeID, expectedStatus, expectedVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) reconcileChallenge(
	ctx context.Context,
	challengeID, expectedStatus, expectedPhase, owner string,
	expectedVersion int64,
	artifactMessageID int,
	lastError string,
	now time.Time,
	requireExpired bool,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	condition := "challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?"
	safeErrorCode := db.SafeGatekeeperErrorText(lastError)
	args := []any{artifactMessageID, safeErrorCode, now, challengeID, expectedStatus, owner, expectedVersion}
	if requireExpired {
		condition = "challenge_id = ? AND status = ? AND action_phase = ? AND action_lease_until <= ?"
		args = []any{artifactMessageID, safeErrorCode, now, challengeID, expectedStatus, expectedPhase, now}
	}
	result, err := tx.ExecContext(
		ctx, `
		INSERT INTO gatekeeper_challenge_reconciliations (
			challenge_id, comm_chat_id, user_id, chat_id, action_status, action_phase,
			artifact_message_id, challenge_message_id, join_message_id, notice_message_id,
			join_request_query_present, web_app_token_present, user_restricted,
			attempt_count, last_error, challenge_created_at, expires_at, effect_started_at,
			reconciliation_due_at
		)
		SELECT challenge_id, comm_chat_id, user_id, chat_id, status, action_phase,
			?, challenge_message_id, join_message_id, notice_message_id,
			join_request_query_id <> '', web_app_token <> '', user_restricted,
			attempt_count + 1, ?, created_at, expires_at, effect_started_at, ?
		FROM gatekeeper_challenges
		WHERE `+condition,
		args...,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return false, err
	}
	deleteArgs := args[3:]
	if _, err := tx.ExecContext(ctx, `DELETE FROM gatekeeper_challenges WHERE `+condition, deleteArgs...); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (c *sqliteClient) CompleteLeasedChallengeActionVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, expectedPhase, nextStatus string,
	expiresAt, now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var nextAttempt any
	if isDurableChallengeActionStatus(nextStatus) {
		nextAttempt = now
	}
	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?, next_attempt_at = ?, attempt_count = 0, last_error = '',
			action_owner = '', action_lease_until = NULL, action_phase = ?, effect_started_at = NULL,
			action_version = action_version + 1,
			expires_at = CASE WHEN ? IS NULL THEN expires_at ELSE ? END
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
	`, nextStatus, nextAttempt, db.ChallengePhaseReady, nullableTime(expiresAt), nullableTime(expiresAt),
		challengeID, expectedStatus, owner, expectedVersion, expectedPhase, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) ScheduleLeasedChallengeRetryVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, expectedPhase string,
	nextAttemptAt time.Time,
	lastError string,
	now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET next_attempt_at = ?, attempt_count = attempt_count + 1, last_error = ?,
			action_owner = '', action_lease_until = NULL, action_phase = ?, effect_started_at = NULL,
			action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
	`, nextAttemptAt, db.SafeGatekeeperErrorText(lastError), db.ChallengePhaseReady, challengeID, expectedStatus, owner, expectedVersion, expectedPhase, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) CompleteLeasedChallengeActivationVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedPhase string,
	restricted bool,
	messageID int,
	now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?, challenge_message_id = ?, user_restricted = ?, next_attempt_at = NULL,
			attempt_count = 0, last_error = '', action_owner = '', action_lease_until = NULL,
			action_phase = ?, effect_started_at = NULL, action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
	`, db.ChallengeStatusPending, messageID, restricted, db.ChallengePhaseReady,
		challengeID, db.ChallengeStatusRestrictPending, owner, expectedVersion, expectedPhase, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) MarkLeasedChallengeRestrictedVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedPhase string,
	now time.Time,
) (int64, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var version int64
	err := c.db.GetContext(ctx, &version, `
		UPDATE gatekeeper_challenges
		SET user_restricted = TRUE, action_phase = ?, action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
		RETURNING action_version
	`, db.ChallengePhaseRestrictDone, challengeID, db.ChallengeStatusRestrictPending, owner, expectedVersion, expectedPhase, now)
	if errors.Is(err, sql.ErrNoRows) {
		return expectedVersion, false, nil
	}
	if err != nil {
		return expectedVersion, false, err
	}
	return version, true, nil
}

func (c *sqliteClient) CompleteLeasedChallengeWithoutPrivilegesVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, expectedPhase string,
	noticeMessageID int,
	expiresAt time.Time,
	lastError string,
	now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?, notice_message_id = ?, expires_at = ?, next_attempt_at = NULL,
			attempt_count = 0, last_error = ?, action_owner = '', action_lease_until = NULL,
			action_phase = ?, effect_started_at = NULL, action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
	`, db.ChallengeStatusNoPrivilegesNotice, noticeMessageID, expiresAt, db.SafeGatekeeperErrorText(lastError), db.ChallengePhaseReady,
		challengeID, expectedStatus, owner, expectedVersion, expectedPhase, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) ArchiveLeasedNoticeFailureVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, expectedPhase string,
	expiresAt time.Time,
	errorCode string,
	now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	safeCode := db.SafeGatekeeperErrorText(errorCode)
	condition := `challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
		AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE`
	args := []any{challengeID, expectedStatus, owner, expectedVersion, expectedPhase, now}
	result, err := tx.ExecContext(
		ctx, `
		INSERT INTO gatekeeper_challenge_reconciliations (
			challenge_id, comm_chat_id, user_id, chat_id, action_status, action_phase,
			challenge_message_id, join_message_id, notice_message_id,
			join_request_query_present, web_app_token_present, user_restricted,
			attempt_count, last_error, challenge_created_at, expires_at, effect_started_at,
			reconciliation_due_at
		)
		SELECT challenge_id, comm_chat_id, user_id, chat_id, status, action_phase,
			challenge_message_id, join_message_id, notice_message_id,
			join_request_query_id <> '', web_app_token <> '', user_restricted,
			attempt_count + 1, ?, created_at, ?, effect_started_at, ?
		FROM gatekeeper_challenges WHERE `+condition,
		append([]any{safeCode, expiresAt, now}, args...)...,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return false, err
	}
	result, err = tx.ExecContext(
		ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?, notice_message_id = 0, expires_at = ?, next_attempt_at = NULL,
			attempt_count = 0, last_error = ?, action_owner = '', action_lease_until = NULL,
			action_phase = ?, effect_started_at = NULL, action_version = action_version + 1
		WHERE `+condition,
		append([]any{db.ChallengeStatusNoPrivilegesNotice, expiresAt, safeCode, db.ChallengePhaseReady}, args...)...,
	)
	if err != nil {
		return false, err
	}
	affected, err = result.RowsAffected()
	if err != nil || affected != 1 {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (c *sqliteClient) DeleteLeasedChallengeActionVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	expectedStatus, expectedPhase string,
	now time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		DELETE FROM gatekeeper_challenges
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
	`, challengeID, expectedStatus, owner, expectedVersion, expectedPhase, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) GetChallengeReconciliations(ctx context.Context) ([]*db.ChallengeReconciliation, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var reconciliations []*db.ChallengeReconciliation
	err := c.db.SelectContext(ctx, &reconciliations, `
		SELECT id, challenge_id, comm_chat_id, user_id, chat_id, action_status, action_phase,
			artifact_message_id, challenge_message_id, join_message_id, notice_message_id,
			join_request_query_present, web_app_token_present, user_restricted, attempt_count,
			last_error, challenge_created_at, expires_at, effect_started_at, reconciliation_due_at,
			retention_until, resolution_status, resolution, resolved_at, version
		FROM gatekeeper_challenge_reconciliations
		ORDER BY resolution_status, reconciliation_due_at, id
	`)
	return reconciliations, err
}

func (c *sqliteClient) ResolveChallengeReconciliation(
	ctx context.Context,
	id, expectedVersion int64,
	resolution string,
	resolvedAt time.Time,
) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenge_reconciliations
		SET resolution_status = ?, resolution = ?, resolved_at = ?, retention_until = ?, version = version + 1
		WHERE id = ? AND version = ? AND resolution_status = ?
	`, db.ChallengeReconciliationResolved, resolution, resolvedAt, resolvedAt.Add(30*24*time.Hour), id, expectedVersion, db.ChallengeReconciliationPending)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) RequeueChallengeReconciliation(
	ctx context.Context,
	id, expectedVersion int64,
	now time.Time,
) (bool, error) {
	_ = ctx
	_ = id
	_ = expectedVersion
	_ = now
	return false, errors.New("requeue is disabled because reconciliation records contain no replay secrets")
}

func (c *sqliteClient) ReconcileExpiredChallengeEffects(ctx context.Context, now time.Time) (int, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	tx, err := c.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO gatekeeper_challenge_reconciliations (
			challenge_id, comm_chat_id, user_id, chat_id, action_status, action_phase,
			challenge_message_id, join_message_id, notice_message_id,
			join_request_query_present, web_app_token_present, user_restricted,
			attempt_count, last_error, challenge_created_at, expires_at, effect_started_at,
			reconciliation_due_at
		)
		SELECT challenge_id, comm_chat_id, user_id, chat_id, status, action_phase,
			challenge_message_id, join_message_id, notice_message_id,
			join_request_query_id <> '', web_app_token <> '', user_restricted,
			attempt_count + 1, 'ambiguous_external_effect',
			created_at, expires_at, effect_started_at, ?
		FROM gatekeeper_challenges
		WHERE action_phase LIKE '%started' AND action_lease_until <= ?
	`, now, now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM gatekeeper_challenges
		WHERE action_phase LIKE '%started' AND action_lease_until <= ?
	`, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func (c *sqliteClient) CleanupResolvedChallengeReconciliations(ctx context.Context, now time.Time) (int, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		DELETE FROM gatekeeper_challenge_reconciliations
		WHERE resolution_status = ? AND retention_until <= ?
	`, db.ChallengeReconciliationResolved, now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func isDurableChallengeActionStatus(status string) bool {
	switch status {
	case db.ChallengeStatusRestrictPending,
		db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending:
		return true
	default:
		return false
	}
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
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
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

func (c *sqliteClient) BeginExpiredWebAppFallback(ctx context.Context, challengeID string) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?, next_attempt_at = ?, attempt_count = 0, last_error = ''
		WHERE challenge_id = ?
			AND status = ?
			AND web_app_token <> ''
			AND join_request_query_id <> ''
			AND expires_at <= ?
	`, db.ChallengeStatusWebAppFallbackPending, time.Now(), challengeID, db.ChallengeStatusPending, time.Now())
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
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
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
		WHERE challenge_id = ? AND status = ? AND join_message_id = 0 AND action_owner = ''
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
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
	`, successUUID, expiresAt, userLanguage, challengeID, db.ChallengeStatusWebAppFallbackPending)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) PrepareDMFallbackVersion(
	ctx context.Context,
	challengeID, owner string,
	expectedVersion int64,
	successUUID, userLanguage string,
	expiresAt, now time.Time,
) (int64, bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var version int64
	err := c.db.GetContext(ctx, &version, `
		UPDATE gatekeeper_challenges
		SET success_uuid = ?, web_app_token = '', captcha_prompt = '', captcha_options_json = '',
			challenge_message_id = 0, attempts = 0, expires_at = ?, user_language = ?,
			next_attempt_at = CURRENT_TIMESTAMP, last_error = '', action_version = action_version + 1
		WHERE challenge_id = ? AND status = ? AND action_owner = ? AND action_version = ?
			AND action_phase = ? AND action_lease_until > ? AND cancel_requested = FALSE
		RETURNING action_version
	`, successUUID, expiresAt, userLanguage, challengeID, db.ChallengeStatusWebAppFallbackPending,
		owner, expectedVersion, db.ChallengePhaseReady, now)
	if errors.Is(err, sql.ErrNoRows) {
		return expectedVersion, false, nil
	}
	if err != nil {
		return expectedVersion, false, err
	}
	return version, true, nil
}

func (c *sqliteClient) CompleteExternalAction(
	ctx context.Context,
	challengeID, expectedStatus, nextStatus string,
	expiresAt time.Time,
) (bool, error) {
	var nextAttemptAt time.Time
	switch nextStatus {
	case db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusRestrictPending,
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
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
	`, nullableTime(nextAttemptAt), db.SafeGatekeeperErrorText(lastError), challengeID, expectedStatus)
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
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
	`, db.ChallengeStatusNoPrivilegesNotice, noticeMessageID, expiresAt, db.SafeGatekeeperErrorText(lastError), challengeID, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (c *sqliteClient) DeleteChallengeInstance(ctx context.Context, challengeID, expectedStatus string) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	result, err := c.db.ExecContext(
		ctx, `
		DELETE FROM gatekeeper_challenges
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
			AND action_owner = ''
			AND status NOT IN (?, ?, ?, ?, ?, ?)
	`, challengeID, expectedStatus,
		db.ChallengeStatusRestrictPending,
		db.ChallengeStatusWebAppFallbackPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending,
	)
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
		WHERE status IN (?, ?, ?, ?, ?, ?)
			AND next_attempt_at IS NOT NULL
			AND next_attempt_at <= ?
		ORDER BY next_attempt_at, created_at
	`,
		db.ChallengeStatusRestrictPending,
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
		WHERE challenge_id = ? AND status = ? AND action_owner = ''
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
			AND status NOT IN (?, ?, ?, ?, ?, ?)
	`,
		now,
		db.ChallengeStatusRestrictPending,
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
