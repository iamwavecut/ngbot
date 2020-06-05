package handlers

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra/reg"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"strings"
)

var allowedLanguages = []string{"en", "ru"}

type Admin struct {
	s bot.Service
}

func NewAdmin(s bot.Service) *Admin {
	a := &Admin{
		s: s,
	}
	return a
}

func (a *Admin) Handle(u *tgbotapi.Update, cm *db.ChatMeta, um *db.UserMeta) (proceed bool, err error) {
	if cm == nil {
		return true, nil
	}

	b := a.s.GetBot()

	switch {
	case
		u.Message == nil,
		um.IsBot,
		!u.Message.IsCommand():
		return true, nil
	}
	m := u.Message

	chatMember, err := b.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			UserID: um.ID,
			ChatID: cm.ID,
		},
	})
	if err != nil {
		return true, errors.New("cant get chat member")
	}
	var isAdmin bool
	switch {
	case
		chatMember.IsCreator(),
		chatMember.IsAdministrator() && chatMember.CanRestrictMembers,
		m.Chat.IsPrivate():
		isAdmin = true
	}

	log.Trace("command: " + m.Command())

	switch m.Command() {
	case "lang":
		if isAdmin {
			log.Trace("admin")

			argument := m.CommandArguments()
			isAllowed := false
			for _, allowedLanguage := range allowedLanguages {
				if allowedLanguage == argument {
					isAllowed = true
					break
				}
			}
			if !isAllowed {
				b.Send(tgbotapi.NewMessage(
					cm.ID,
					i18n.Get("You should use one of the following options", cm.Language)+": "+strings.Join(allowedLanguages, ", "),
				))
				return false, nil
			}

			cm.Language = argument
			err = a.s.GetDB().UpsertChatMeta(cm)
			if err != nil {
				return false, errors.WithMessage(err, "cant update chat language")
			}
			reg.Get().RemoveCM(cm.ID)

			b.Send(tgbotapi.NewMessage(
				cm.ID,
				i18n.Get("Language set successfully", cm.Language),
			))
		}
		log.Trace("not admin")
		return false, nil
	}
	log.Trace("unknown command")
	return true, nil
}

func (a *Admin) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}
