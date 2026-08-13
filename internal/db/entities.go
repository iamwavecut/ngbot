package db

import (
	"database/sql"
	"time"
)

type (
	Settings struct {
		ID                                      int64  `db:"id"`
		Revision                                int64  `db:"settings_revision"`
		Language                                string `db:"language"`
		Enabled                                 bool   `db:"enabled"`
		GatekeeperEnabled                       bool   `db:"gatekeeper_enabled"`
		GatekeeperCaptchaEnabled                bool   `db:"gatekeeper_captcha_enabled"`
		GatekeeperGreetingEnabled               bool   `db:"gatekeeper_greeting_enabled"`
		GatekeeperCaptchaOptionsCount           int    `db:"gatekeeper_captcha_options_count"`
		GatekeeperGreetingText                  string `db:"gatekeeper_greeting_text"`
		LLMFirstMessageEnabled                  bool   `db:"llm_first_message_enabled"`
		ReactionProfileCheckEnabled             bool   `db:"reaction_profile_check_enabled"`
		CommunityVotingEnabled                  bool   `db:"community_voting_enabled"`
		CommunityVotingTimeoutOverrideNS        int64  `db:"community_voting_timeout_override_ns"`
		CommunityVotingMinVotersOverride        int    `db:"community_voting_min_voters_override"`
		CommunityVotingMaxVotersOverride        int    `db:"community_voting_max_voters_override"`
		CommunityVotingMinVotersPercentOverride int    `db:"community_voting_min_voters_percent_override"`
		ChallengeTimeout                        int64  `db:"challenge_timeout"`
		RejectTimeout                           int64  `db:"reject_timeout"`
	}

	SpamCase struct {
		ID                    int64        `db:"id"`
		ChatID                int64        `db:"chat_id"`
		UserID                int64        `db:"user_id"`
		MessageID             int          `db:"message_id"`
		MessageText           string       `db:"message_text"`
		CreatedAt             time.Time    `db:"created_at"`
		ChannelUsername       string       `db:"channel_username"`
		ChannelPostID         int          `db:"channel_post_id"`
		NotificationMessageID int          `db:"notification_message_id"`
		PreVoteRestricted     bool         `db:"pre_vote_restricted"`
		Status                string       `db:"status"` // pending, spam, false_positive
		ResolvedAt            *time.Time   `db:"resolved_at"`
		ResolveAt             *time.Time   `db:"resolve_at"`
		NextAttemptAt         sql.NullTime `db:"next_attempt_at"`
		AttemptCount          int          `db:"attempt_count"`
		LastError             string       `db:"last_error"`
	}

	SpamCaseReportMessage struct {
		CaseID    int64     `db:"case_id"`
		ChatID    int64     `db:"chat_id"`
		MessageID int       `db:"message_id"`
		CreatedAt time.Time `db:"created_at"`
	}

	SpamVote struct {
		CaseID  int64     `db:"case_id"`
		VoterID int64     `db:"voter_id"`
		Vote    bool      `db:"vote"` // true = not spam, false = spam
		VotedAt time.Time `db:"voted_at"`
	}

	RecentJoiner struct {
		ID            int64     `db:"id"`
		JoinMessageID int       `db:"join_message_id"`
		ChatID        int64     `db:"chat_id"`
		UserID        int64     `db:"user_id"`
		Username      string    `db:"username"`
		JoinedAt      time.Time `db:"joined_at"`
		Processed     bool      `db:"processed"`
		IsSpammer     bool      `db:"is_spammer"`
	}

	Challenge struct {
		ChallengeID        string       `db:"challenge_id"`
		CommChatID         int64        `db:"comm_chat_id"`
		UserID             int64        `db:"user_id"`
		ChatID             int64        `db:"chat_id"`
		Status             string       `db:"status"`
		SuccessUUID        string       `db:"success_uuid"`
		WebAppToken        string       `db:"web_app_token"`
		JoinRequestQueryID string       `db:"join_request_query_id"`
		CaptchaPrompt      string       `db:"captcha_prompt"`
		CaptchaOptionsJSON string       `db:"captcha_options_json"`
		JoinMessageID      int          `db:"join_message_id"`
		ChallengeMessageID int          `db:"challenge_message_id"`
		NoticeMessageID    int          `db:"notice_message_id"`
		UserRestricted     bool         `db:"user_restricted"`
		Attempts           int          `db:"attempts"`
		CreatedAt          time.Time    `db:"created_at"`
		ExpiresAt          time.Time    `db:"expires_at"`
		WebAppOpenedAt     sql.NullTime `db:"web_app_opened_at"`
		UserLanguage       string       `db:"user_language"`
		NextAttemptAt      sql.NullTime `db:"next_attempt_at"`
		AttemptCount       int          `db:"attempt_count"`
		LastError          string       `db:"last_error"`
		ActionOwner        string       `db:"action_owner"`
		ActionLeaseUntil   sql.NullTime `db:"action_lease_until"`
		ActionVersion      int64        `db:"action_version"`
		ActionPhase        string       `db:"action_phase"`
		EffectStartedAt    sql.NullTime `db:"effect_started_at"`
		CancelRequested    bool         `db:"cancel_requested"`
	}

	ChallengeReconciliation struct {
		ID                      int64        `db:"id"`
		ChallengeID             string       `db:"challenge_id"`
		CommChatID              int64        `db:"comm_chat_id"`
		UserID                  int64        `db:"user_id"`
		ChatID                  int64        `db:"chat_id"`
		ActionStatus            string       `db:"action_status"`
		ActionPhase             string       `db:"action_phase"`
		ArtifactMessageID       int          `db:"artifact_message_id"`
		ChallengeMessageID      int          `db:"challenge_message_id"`
		JoinMessageID           int          `db:"join_message_id"`
		NoticeMessageID         int          `db:"notice_message_id"`
		JoinRequestQueryPresent bool         `db:"join_request_query_present"`
		WebAppTokenPresent      bool         `db:"web_app_token_present"`
		UserRestricted          bool         `db:"user_restricted"`
		AttemptCount            int          `db:"attempt_count"`
		LastError               string       `db:"last_error"`
		ChallengeCreatedAt      time.Time    `db:"challenge_created_at"`
		ExpiresAt               time.Time    `db:"expires_at"`
		EffectStartedAt         sql.NullTime `db:"effect_started_at"`
		ReconciliationDueAt     time.Time    `db:"reconciliation_due_at"`
		RetentionUntil          sql.NullTime `db:"retention_until"`
		ResolutionStatus        string       `db:"resolution_status"`
		Resolution              string       `db:"resolution"`
		ResolvedAt              sql.NullTime `db:"resolved_at"`
		Version                 int64        `db:"version"`
	}

	ChatManager struct {
		ChatID             int64     `db:"chat_id"`
		UserID             int64     `db:"user_id"`
		CanManageChat      bool      `db:"can_manage_chat"`
		CanPromoteMembers  bool      `db:"can_promote_members"`
		CanRestrictMembers bool      `db:"can_restrict_members"`
		UpdatedAt          time.Time `db:"updated_at"`
	}

	ChatBotMembership struct {
		ChatID    int64     `db:"chat_id"`
		IsMember  bool      `db:"is_member"`
		UpdatedAt time.Time `db:"updated_at"`
	}

	AdminPanelSession struct {
		ID        int64     `db:"id"`
		UserID    int64     `db:"user_id"`
		ChatID    int64     `db:"chat_id"`
		Page      string    `db:"page"`
		StateJSON string    `db:"state_json"`
		MessageID int       `db:"message_id"`
		CreatedAt time.Time `db:"created_at"`
		UpdatedAt time.Time `db:"updated_at"`
	}

	AdminPanelCommand struct {
		ID        int64     `db:"id"`
		SessionID int64     `db:"session_id"`
		Payload   string    `db:"payload"`
		CreatedAt time.Time `db:"created_at"`
	}

	ChatSpamExample struct {
		ID              int64     `db:"id"`
		ChatID          int64     `db:"chat_id"`
		Text            string    `db:"text"`
		CreatedByUserID int64     `db:"created_by_user_id"`
		CreatedAt       time.Time `db:"created_at"`
	}

	ChatKnownNonMember struct {
		ChatID    int64     `db:"chat_id"`
		UserID    int64     `db:"user_id"`
		CreatedAt time.Time `db:"created_at"`
		UpdatedAt time.Time `db:"updated_at"`
	}

	MessageProbation struct {
		ChatID      int64        `db:"chat_id"`
		UserID      int64        `db:"user_id"`
		StartedAt   time.Time    `db:"started_at"`
		EligibleAt  time.Time    `db:"eligible_at"`
		GraduatedAt sql.NullTime `db:"graduated_at"`
	}

	TelegramUpdate struct {
		UpdateID         int          `db:"update_id"`
		DispatchKey      string       `db:"dispatch_key"`
		Payload          []byte       `db:"payload"`
		SecurityRelevant bool         `db:"security_relevant"`
		Status           string       `db:"status"`
		AttemptCount     int          `db:"attempt_count"`
		AvailableAt      time.Time    `db:"available_at"`
		ReceivedAt       time.Time    `db:"received_at"`
		StartedAt        sql.NullTime `db:"started_at"`
		CompletedAt      sql.NullTime `db:"completed_at"`
		LastError        string       `db:"last_error"`
		OutcomeSource    string       `db:"outcome_source"`
		LeaseOwner       string       `db:"lease_owner"`
		LeaseVersion     int64        `db:"lease_version"`
		LeaseUntil       sql.NullTime `db:"lease_until"`
	}

	TelegramUpdateFailure struct {
		ID               int64        `db:"id"`
		UpdateID         int          `db:"update_id"`
		DispatchKey      string       `db:"dispatch_key"`
		SecurityRelevant bool         `db:"security_relevant"`
		AttemptCount     int          `db:"attempt_count"`
		FailureSource    string       `db:"failure_source"`
		FailureReason    string       `db:"failure_reason"`
		LastError        string       `db:"last_error"`
		CreatedAt        time.Time    `db:"created_at"`
		ResolvedAt       sql.NullTime `db:"resolved_at"`
	}

	ChatNotSpammerOverride struct {
		ID              int64     `db:"id"`
		ChatID          int64     `db:"chat_id"`
		MatchType       string    `db:"match_type"`
		MatchValue      string    `db:"match_value"`
		CreatedByUserID int64     `db:"created_by_user_id"`
		CreatedAt       time.Time `db:"created_at"`
	}
)

const (
	defaultChallengeTimeout                = 3 * time.Minute
	defaultRejectTimeout                   = 10 * time.Minute
	legacySecondDurationCeiling            = int64(time.Second)
	NotSpammerMatchTypeUserID              = "user_id"
	NotSpammerMatchTypeUsername            = "username"
	ChallengeStatusPending                 = "pending"
	ChallengeStatusBanCheckPending         = "ban_check_pending"
	ChallengeStatusPassedWaitingMemberJoin = "passed_waiting_member_join"
	ChallengeStatusWebAppFallbackPending   = "web_app_fallback_pending"
	ChallengeStatusRestrictPending         = "restrict_pending"
	ChallengeStatusApproveQueryPending     = "approve_query_pending"
	ChallengeStatusApproveMemberPending    = "approve_member_pending"
	ChallengeStatusUnrestrictPending       = "unrestrict_pending"
	ChallengeStatusRejectPending           = "reject_pending"
	ChallengeStatusNoPrivilegesNotice      = "no_privileges_notice"
	SpamCaseStatusPending                  = "pending"
	SpamCaseStatusResolvingSpam            = "resolving_spam"
	SpamCaseStatusResolvingFalsePositive   = "resolving_false_positive"
	SpamCaseStatusSpam                     = "spam"
	SpamCaseStatusFalsePositive            = "false_positive"
	SpamCaseStatusNotEnforced              = "not_enforced"
	TelegramUpdateStatusPending            = "pending"
	TelegramUpdateStatusProcessing         = "processing"
	TelegramUpdateStatusRetry              = "retry"
	TelegramUpdateStatusCompleted          = "completed"
	TelegramUpdateStatusDeadLetter         = "dead_letter"
)

const (
	ChallengePhaseReady                  = "ready"
	ChallengePhaseQueueResponseStarted   = "queue_response_started"
	ChallengePhaseQueueResponseDone      = "queue_response_done"
	ChallengePhaseWebAppResponseStarted  = "webapp_response_started"
	ChallengePhaseWebAppResponseDone     = "webapp_response_done"
	ChallengePhaseRestrictStarted        = "restrict_started"
	ChallengePhaseRestrictDone           = "restrict_done"
	ChallengePhasePublicMessageStarted   = "public_message_started"
	ChallengePhasePublicMessageDone      = "public_message_done"
	ChallengePhaseFallbackMessageStarted = "fallback_message_started"
	ChallengePhaseFallbackMessageDone    = "fallback_message_done"
	ChallengePhaseQueryAnswerStarted     = "query_answer_started"
	ChallengePhaseQueryAnswerDone        = "query_answer_done"
	ChallengePhaseMemberApprovalStarted  = "member_approval_started"
	ChallengePhaseMemberApprovalDone     = "member_approval_done"
	ChallengePhaseUnrestrictStarted      = "unrestrict_started"
	ChallengePhaseUnrestrictDone         = "unrestrict_done"
	ChallengePhaseNoticeMessageStarted   = "notice_message_started"
	ChallengePhaseNoticeMessageDone      = "notice_message_done"
	ChallengePhaseRejectProbeDone        = "reject_probe_done"
	ChallengePhaseRejectBanStarted       = "reject_ban_started"
	ChallengePhaseRejectBanDone          = "reject_ban_done"
	ChallengePhaseRejectDeclineStarted   = "reject_decline_started"
	ChallengePhaseRejectDeclineDone      = "reject_decline_done"
)

const (
	ChallengeReconciliationPending  = "pending"
	ChallengeReconciliationResolved = "resolved"
)

// GetLanguage Returns chat's set language
func (cm *Settings) GetLanguage() (string, error) {
	if cm == nil {
		return "en", nil
	}
	if cm.Language == "" {
		return "en", nil
	}
	return cm.Language, nil
}

// GetChallengeTimeout Returns chat entry challenge timeout duration
func (cm *Settings) GetChallengeTimeout() time.Duration {
	if cm == nil {
		return defaultChallengeTimeout
	}
	return time.Duration(normalizeTimeoutNS(cm.ChallengeTimeout, defaultChallengeTimeout))
}

// GetRejectTimeout Returns chat entry reject timeout duration
func (cm *Settings) GetRejectTimeout() time.Duration {
	if cm == nil {
		return defaultRejectTimeout
	}
	return time.Duration(normalizeTimeoutNS(cm.RejectTimeout, defaultRejectTimeout))
}

func normalizeTimeoutNS(value int64, fallback time.Duration) int64 {
	if value == 0 {
		return fallback.Nanoseconds()
	}
	if value > 0 && value < legacySecondDurationCeiling {
		return (time.Duration(value) * time.Second).Nanoseconds()
	}
	return value
}
