package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"

	"github.com/iamwavecut/ngbot/internal/config"
)

type (
	ChatMeta struct {
		ID       int64        `db:"id"`
		Title    string       `db:"title"`
		Language string       `db:"language"`
		Type     string       `db:"type"`
		Settings ChatSettings `db:"settings"`
	}

	UserMeta struct {
		ID           int64  `db:"id"`
		FirstName    string `db:"first_name"`
		LastName     string `db:"last_name"`
		UserName     string `db:"username"`
		LanguageCode string `db:"language_code"`
		IsBot        bool   `db:"is_bot"`
	}

	CharadeScore struct {
		UserID int64 `db:"user_id"`
		ChatID int64 `db:"chat_id"`
		Score  int   `db:"score"`
	}

	ChatSettings map[string]string
)

const (
	language         = "language"
	challengeTimeout = "challenge_timeout"
	rejectTimeout    = "reject_timeout"

	defaultChallengeTimeout = 3 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute
)

var DefaultChatSettings = &ChatSettings{
	challengeTimeout: defaultChallengeTimeout.String(),
	rejectTimeout:    defaultRejectTimeout.String(),
}

func (s *ChatSettings) Value() (driver.Value, error) {
	return json.Marshal(s)
}

func (s *ChatSettings) Scan(v interface{}) error {
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
func (cm *ChatMeta) GetLanguage() (string, error) {
	if cm == nil {
		return "", errors.New("nil chat")
	}
	if cm.Settings == nil {
		cm.Settings = ChatSettings{}
		cm.Settings = *DefaultChatSettings
		cm.Settings[language] = config.Get().DefaultLanguage
	}

	if language, ok := cm.Settings[language]; ok {
		return language, nil
	}
	return config.Get().DefaultLanguage, errors.New("no language set")
}

// GetChallengeTimeout Returns chat entry challenge timeout duration
func (cm *ChatMeta) GetChallengeTimeout() time.Duration {
	if cm == nil {
		return defaultChallengeTimeout
	}
	if cm.Settings == nil {
		cm.Settings = ChatSettings{}
	}

	if ctStr, ok := cm.Settings[challengeTimeout]; ok {
		if ct, err := time.ParseDuration(ctStr); err == nil {
			return ct
		}
	}
	cm.Settings[challengeTimeout] = defaultChallengeTimeout.String()
	return defaultChallengeTimeout
}

// GetRejectTimeout Returns chat entry reject timeout duration
func (cm *ChatMeta) GetRejectTimeout() time.Duration {
	if cm == nil {
		return defaultRejectTimeout
	}
	if cm.Settings == nil {
		cm.Settings = ChatSettings{}
	}

	if rtStr, ok := cm.Settings[rejectTimeout]; !ok {
		if rt, err := time.ParseDuration(rtStr); err == nil {
			return rt
		}
	}
	cm.Settings[rejectTimeout] = defaultRejectTimeout.String()

	return defaultRejectTimeout
}
