package bot

import (
	"context"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

// ServiceBot defines bot-specific operations
type ServiceBot interface {
	GetBot() *api.BotAPI
}

// ServiceDB defines database-specific operations
type ServiceDB interface {
	GetDB() db.Client
}

// Service defines the core bot service interface
type Service interface {
	ServiceBot
	ServiceDB
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
	InsertMember(ctx context.Context, chatID, userID int64) error
	GetSettings(ctx context.Context, chatID int64) (*db.Settings, error)
	SetSettings(ctx context.Context, settings *db.Settings) error
	GetLanguage(ctx context.Context, chatID int64, user *api.User) string
}

// Handler defines the interface for all update handlers in the system
type Handler interface {
	Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
}

// Client defines the database interface
type Client interface {
	GetSettings(ctx context.Context, chatID int64) (*db.Settings, error)
	SetSettings(ctx context.Context, settings *db.Settings) error
	
	// Spam control methods
	CreateSpamCase(ctx context.Context, sc *db.SpamCase) (*db.SpamCase, error)
	UpdateSpamCase(ctx context.Context, sc *db.SpamCase) error
	GetSpamCase(ctx context.Context, id int64) (*db.SpamCase, error)
	GetSpamVotes(ctx context.Context, caseID int64) ([]*db.SpamVote, error)
	AddSpamVote(ctx context.Context, vote *db.SpamVote) error
} 
