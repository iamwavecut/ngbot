package db

import (
	"time"

	"github.com/iamwavecut/ngbot/internal/config"
)

type (
	Settings struct {
		ID               int64  `db:"id"`
		Language         string `db:"language"`
		Enabled          bool   `db:"enabled"`
		ChallengeTimeout int64  `db:"challenge_timeout"`
		RejectTimeout    int64  `db:"reject_timeout"`
	}

	SpamCase struct {
		ID                    int64      `db:"id"`
		ChatID                int64      `db:"chat_id"`
		UserID                int64      `db:"user_id"`
		MessageText           string     `db:"message_text"`
		CreatedAt             time.Time  `db:"created_at"`
		ChannelUsername       string     `db:"channel_username"`
		ChannelPostID         int        `db:"channel_post_id"`
		NotificationMessageID int        `db:"notification_message_id"`
		Status                string     `db:"status"` // pending, spam, false_positive
		ResolvedAt            *time.Time `db:"resolved_at"`
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
)

const (
	defaultChallengeTimeout = 3 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute
)

// GetLanguage Returns chat's set language
func (cm *Settings) GetLanguage() (string, error) {
	if cm == nil {
		return config.Get().DefaultLanguage, nil
	}
	if cm.Language == "" {
		return config.Get().DefaultLanguage, nil
	}
	return cm.Language, nil
}

// GetChallengeTimeout Returns chat entry challenge timeout duration
func (cm *Settings) GetChallengeTimeout() time.Duration {
	if cm == nil {
		return defaultChallengeTimeout
	}
	if cm.ChallengeTimeout == 0 {
		cm.ChallengeTimeout = defaultChallengeTimeout.Nanoseconds()
	}
	return time.Duration(cm.ChallengeTimeout)
}

// GetRejectTimeout Returns chat entry reject timeout duration
func (cm *Settings) GetRejectTimeout() time.Duration {
	if cm == nil {
		return defaultRejectTimeout
	}
	if cm.RejectTimeout == 0 {
		cm.RejectTimeout = defaultRejectTimeout.Nanoseconds()
	}
	return time.Duration(cm.RejectTimeout)
}
