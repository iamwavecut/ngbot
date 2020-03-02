package bot

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/config"
	"github.com/iamwavecut/ngbot/db"
)

type ServiceBot interface {
	GetBot() *tgbotapi.BotAPI
}

type ServiceDB interface {
	GetDB() db.Client
}

type ServiceConfig interface {
	GetConfig() *config.Config
}

type Service interface {
	ServiceBot
	ServiceDB
	ServiceConfig
}

type service struct {
	bot *tgbotapi.BotAPI
	db  db.Client
	cfg *config.Config
}

func NewService(bot *tgbotapi.BotAPI, db db.Client, cfg *config.Config) *service {
	return &service{
		bot: bot,
		db:  db,
		cfg: cfg,
	}
}

func (s *service) GetBot() *tgbotapi.BotAPI {
	return s.bot
}

func (s *service) GetDB() db.Client {
	return s.db
}

func (s *service) GetConfig() *config.Config {
	return s.cfg
}
