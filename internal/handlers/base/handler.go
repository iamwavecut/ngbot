package base

import (
	"context"
	"errors"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const (
	defaultChallengeTimeout = 5 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute
)

// BaseHandler provides common functionality for all handlers
type BaseHandler struct {
	service bot.Service
	logger  *log.Entry
}

// NewBaseHandler creates a new base handler
func NewBaseHandler(service bot.Service, handlerName string) *BaseHandler {
	return &BaseHandler{
		service: service,
		logger:  log.WithField("handler", handlerName),
	}
}

// GetService returns the bot service
func (h *BaseHandler) GetService() bot.Service {
	return h.service
}

// GetLogger returns the handler's logger
func (h *BaseHandler) GetLogger() *log.Entry {
	return h.logger
}

// ValidateUpdate performs common update validation
func (h *BaseHandler) ValidateUpdate(u *api.Update, chat *api.Chat, user *api.User) error {
	if u == nil {
		return ErrNilUpdate
	}
	if chat == nil || user == nil {
		return ErrNilChatOrUser
	}
	return nil
}

// GetOrCreateSettings retrieves or creates default settings for a chat
func (h *BaseHandler) GetOrCreateSettings(ctx context.Context, chat *api.Chat) (*db.Settings, error) {
	settings, err := h.service.GetSettings(ctx, chat.ID)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		settings = &db.Settings{
			ID:               chat.ID,
			Enabled:          true,
			ChallengeTimeout: defaultChallengeTimeout.Nanoseconds(),
			RejectTimeout:    defaultRejectTimeout.Nanoseconds(),
			Language:         "en",
		}
		if err := h.service.SetSettings(ctx, settings); err != nil {
			return nil, err
		}
	}
	return settings, nil
}

// GetLanguage returns the language for a chat/user
func (h *BaseHandler) GetLanguage(ctx context.Context, chat *api.Chat, user *api.User) string {
	return h.service.GetLanguage(ctx, chat.ID, user)
}

var (
	ErrNilUpdate     = errors.New("nil update")
	ErrNilChatOrUser = errors.New("nil chat or user")
) 
