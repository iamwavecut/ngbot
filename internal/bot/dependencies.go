package bot

import (
	"context"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

// Service defines the core bot service interface
type Service interface {
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
	InsertMember(ctx context.Context, chatID, userID int64) error
	DeleteMember(ctx context.Context, chatID, userID int64) error
	GetSettings(ctx context.Context, chatID int64) (*db.Settings, error)
	SetSettings(ctx context.Context, settings *db.Settings) error
	GetLanguage(ctx context.Context, chatID int64, user *api.User) string
}

// Handler defines the interface for all update handlers in the system
type Handler interface {
	Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
}
