package handlers

import (
	"context"

	api "github.com/OvyFlash/telegram-bot-api"
)

type Handler interface {
	Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
}
