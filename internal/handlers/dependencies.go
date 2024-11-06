package handlers

import (
	api "github.com/OvyFlash/telegram-bot-api/v6"
)

type Handler interface {
	Handle(u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
}
