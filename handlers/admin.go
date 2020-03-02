package handlers

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/bot"
	"github.com/iamwavecut/ngbot/db"
	log "github.com/sirupsen/logrus"
)

type Admin struct {
	s bot.Service
}

func NewAdmin(s bot.Service) *Admin {
	a := &Admin{
		s: s,
	}
	return a
}

func (a *Admin) Handle(u *tgbotapi.Update, chatMeta *db.ChatMeta) (proceed bool, err error) {
	return true, nil
}

func (a *Admin) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}
