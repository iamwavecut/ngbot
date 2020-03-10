package db

import (
	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	log "github.com/sirupsen/logrus"
	"strings"
)

type (
	ChatMeta struct {
		ID       int64  `db:"id"`
		Title    string `db:"title"`
		Language string `db:"language"`
		Type     string `db:"type"`
	}

	UserMeta struct {
		ID           int    `db:"id"`
		FirstName    string `db:"first_name"`
		LastName     string `db:"last_name"`
		UserName     string `db:"username"`
		LanguageCode string `db:"language_code"`
		IsBot        bool   `db:"is_bot"`
	}

	CharadeScore struct {
		UserID int   `db:"user_id"`
		ChatID int64 `db:"chat_id"`
		Score  int   `db:"score"`
	}
)

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

func MetaFromChat(chat *api.Chat) *ChatMeta {
	return &ChatMeta{
		ID:       chat.ID,
		Title:    getChatTitle(chat),
		Language: "en",
		Type:     chat.Type,
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
