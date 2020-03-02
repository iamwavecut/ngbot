package handlers

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/db"
)

type Handler interface {
	Handle(u *tgbotapi.Update, chatMeta *db.ChatMeta) (proceed bool, err error)
}
