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
