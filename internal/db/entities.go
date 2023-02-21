package db

import (
	"strings"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

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

// GetLanguage Returns chat's set language
func (cm *ChatMeta) GetLanguage() (string, error) {
	if cm == nil {
		return "", errors.New("nill chat")
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
		Settings: ChatSettings{
			language:         defaultLanguage,
			challengeTimeout: defaultChallengeTimeout.String(),
			rejectTimeout:    defaultRejectTimeout.String(),
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
