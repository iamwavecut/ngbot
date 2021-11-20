package bot

import (
	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

type ServiceBot interface {
	GetBot() *api.BotAPI
}

type ServiceDB interface {
	GetDB() db.Client
}

type Service interface {
	ServiceBot
	ServiceDB
}

type service struct {
	bot *api.BotAPI
	db  db.Client
	cfg *config.Config
}

func NewService(bot *api.BotAPI, db db.Client) *service {
	return &service{
		bot: bot,
		db:  db,
	}
}

func (s *service) GetBot() *api.BotAPI {
	return s.bot
}

func (s *service) GetDB() db.Client {
	return s.db
}
