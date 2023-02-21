package handlers

import (
	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Handler interface {
	Handle(u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
}
