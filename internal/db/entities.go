package db

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/iamwavecut/ngbot/internal/config"
)

type (
	Settings struct {
		ID               int64         `db:"id"`
		Language         string        `db:"language"`
		Enabled          bool          `db:"enabled"`
		ChallengeTimeout time.Duration `db:"challenge_timeout"`
		RejectTimeout    time.Duration `db:"reject_timeout"`
	}
)

const (
	defaultChallengeTimeout = 3 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute
)

func (s *Settings) Scan(v interface{}) error {
	if v == nil {
		return nil
	}
	switch data := v.(type) {
	case string:
		return json.Unmarshal([]byte(data), &s)
	case []byte:
		return json.Unmarshal(data, &s)
	default:
		return fmt.Errorf("cannot scan type %t into Dict", v)
	}
}

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
		cm.ChallengeTimeout = defaultChallengeTimeout
	}
	return cm.ChallengeTimeout
}

// GetRejectTimeout Returns chat entry reject timeout duration
func (cm *Settings) GetRejectTimeout() time.Duration {
	if cm == nil {
		return defaultRejectTimeout
	}
	if cm.RejectTimeout == 0 {
		cm.RejectTimeout = defaultRejectTimeout
	}

	return cm.RejectTimeout
}
