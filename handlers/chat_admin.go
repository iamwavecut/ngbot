package handlers

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/config"
	log "github.com/sirupsen/logrus"
)

type ChatAdmin struct {
	cfg *config.Config
	bot *tgbotapi.BotAPI
}

func NewChatAdmin(cfg *config.Config, bot *tgbotapi.BotAPI) *ChatAdmin {
	ca := &ChatAdmin{
		cfg: cfg,
		bot: bot,
	}

	return ca
}

func (ca *ChatAdmin) getLogEntry() *log.Entry {
	return log.WithField("context", "chat_admin")
}
