package handlers

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/bot"
	"github.com/iamwavecut/ngbot/db"
	log "github.com/sirupsen/logrus"
)

type Charades struct {
	s    bot.Service
	data map[string]map[string]string
}

func NewCharades(s bot.Service) *Charades {
	c := &Charades{
		s: s,
	}
	return c
}

func (c *Charades) Handle(u *tgbotapi.Update, chatMeta *db.ChatMeta) (proceed bool, err error) {
	return true, nil
}

func (c *Charades) openGZIP() {

}

func (c *Charades) open() {

}

func (c *Charades) getLogEntry() *log.Entry {
	return log.WithField("context", "charades")
}
