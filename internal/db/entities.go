package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/config"
)

type (
	ChatMeta struct {
		ID       int64         `db:"id"`
		Title    string        `db:"title"`
		Language string        `db:"language"`
		Type     string        `db:"type"`
		Settings *ChatSettings `db:"settings"`
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

	ChatSettings struct {
		cs map[string]string
	}
)

const (
	language         = "language"
	challengeTimeout = "challenge_timeout"
	rejectTimeout    = "reject_timeout"

	defaultChallengeTimeout = 3 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute
)

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

func (s *ChatSettings) Get(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	if s.cs == nil {
		s.cs = map[string]string{}
	}
	str, ok := s.cs[key]
	return str, ok
}

func (s *ChatSettings) Set(key, value string) {
	if s == nil {
		return
	}
	if s.cs == nil {
		s.cs = map[string]string{}
	}
	s.cs[key] = value
}

// GetLanguage Returns chat's set language
func (cm *ChatMeta) GetLanguage() (string, error) {
	if cm == nil {
		return "", errors.New("nill chat")
	}
	if cm.Settings == nil {
		lang := config.Get().DefaultLanguage
		cm.Settings = &ChatSettings{cs: map[string]string{}}
		cm.Settings.Set(language, lang)
		return lang, nil
	}
	language, ok := cm.Settings.Get(language)
	if !ok {
		return config.Get().DefaultLanguage, errors.New("no language set")
	}
	return language, nil
}

// GetChallengeTimeout Returns chat entry challenge timeout duration
func (cm *ChatMeta) GetChallengeTimeout() time.Duration {
	if cm == nil {
		return defaultChallengeTimeout
	}
	if cm.Settings == nil {
		cm.Settings = &ChatSettings{cs: map[string]string{}}
	}
	ctStr, ok := cm.Settings.Get(challengeTimeout)
	if !ok {
		cm.Settings.Set(challengeTimeout, defaultChallengeTimeout.String())
		return defaultChallengeTimeout
	}
	if ct, err := time.ParseDuration(ctStr); err == nil {
		return ct
	}
	return defaultChallengeTimeout
}

// GetRejectTimeout Returns chat entry reject timeout duration
func (cm *ChatMeta) GetRejectTimeout() time.Duration {
	if cm == nil {
		return defaultRejectTimeout
	}
	if cm.Settings == nil {
		cm.Settings = &ChatSettings{cs: map[string]string{}}
	}
	rtStr, ok := cm.Settings.Get(rejectTimeout)
	if !ok {
		cm.Settings.Set(rejectTimeout, defaultRejectTimeout.String())
		return defaultRejectTimeout
	}
	if rt, err := time.ParseDuration(rtStr); err == nil {
		return rt
	}
	return defaultRejectTimeout
}

func (um *UserMeta) GetFullName() string {
	fullName := um.FirstName + " " + um.LastName
	fullName = strings.TrimSpace(fullName)
	if 0 == len(fullName) {
		fullName = um.UserName
	}
	return fullName
}

func (um *UserMeta) GetUN() string {
	userName := um.UserName
	if 0 == len(userName) {
		userName = um.FirstName + " " + um.LastName
		userName = strings.TrimSpace(userName)
	}
	return userName
}

func MetaFromChat(chat *api.Chat, defaultLanguage string) *ChatMeta {
	return &ChatMeta{
		ID:    chat.ID,
		Title: getChatTitle(chat),
		Type:  chat.Type,
		Settings: &ChatSettings{
			cs: map[string]string{
				language:         defaultLanguage,
				challengeTimeout: defaultChallengeTimeout.String(),
				rejectTimeout:    defaultRejectTimeout.String(),
			},
		},
	}
}

func MetaFromUser(user *api.User) *UserMeta {
	return &UserMeta{
		ID:           user.ID,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		UserName:     user.UserName,
		LanguageCode: user.LanguageCode,
		IsBot:        user.IsBot,
	}
}

func getChatTitle(chat *api.Chat) string {
	if chat == nil {
		return ""
	}
	switch chat.Type {
	case "private":
		return "p2p"
	case "supergroup", "group", "channel":
		return chat.Title
	default:
		log.Warn("unknown chat type", chat.Type)
	}

	return ""
}
