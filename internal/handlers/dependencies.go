package handlers

import (
	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/iamwavecut/ngbot/internal/db"
)

type Handler interface {
	Handle(u *api.Update, cm *db.ChatMeta, um *db.UserMeta) (proceed bool, err error)
}
