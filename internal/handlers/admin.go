package handlers

import (
	"database/sql"
	"strings"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type Admin struct {
	s         bot.Service
	languages []string
}

func NewAdmin(s bot.Service) *Admin {
	a := &Admin{
		s:         s,
		languages: i18n.GetLanguagesList(),
	}

	return a
}

func (a *Admin) Handle(u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	if chat == nil || user == nil {
		return true, nil
	}

	b := a.s.GetBot()

	switch {
	case
		u.Message == nil,
		user.IsBot,
		!u.Message.IsCommand():
		return true, nil
	}
	m := u.Message

	chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			UserID: user.ID,
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
		},
	})
	if err != nil {
		return true, errors.New("cant get chat member")
	}
	var isAdmin bool
	switch {
	case
		chatMember.IsCreator(),
		chatMember.IsAdministrator() && chatMember.CanRestrictMembers:
		isAdmin = true
	}
	entry := a.getLogEntry()

	entry.Trace("command:", m.Command())
	lang, err := a.s.GetDB().GetChatLanguage(chat.ID)
	if tool.Try(err) {
		if errors.Cause(err) != sql.ErrNoRows {
			return true, errors.WithMessage(err, "cant get chat language")
		}
	}
	if lang == "" {
		lang = config.Get().DefaultLanguage
	}
	switch m.Command() {
	case "lang":
		if !isAdmin {
			entry.Trace("not admin")
			break
		}

		argument := m.CommandArguments()
		isAllowed := false
		for _, allowedLanguage := range a.languages {
			log.Traceln(allowedLanguage, argument)
			if allowedLanguage == argument {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			msg := api.NewMessage(
				chat.ID,
				i18n.Get("You should use one of the following options", lang)+": `"+strings.Join(a.languages, "`, `")+"`",
			)
			msg.ParseMode = api.ModeMarkdown
			msg.DisableNotification = true
			_, _ = b.Send(msg)
			return false, nil
		}

		lang = argument
		err = a.s.GetDB().SetChatLanguage(chat.ID, lang)
		if tool.Try(err) {
			return false, errors.WithMessage(err, "cant update chat language")
		}

		_, _ = b.Send(api.NewMessage(
			chat.ID,
			i18n.Get("Language set successfully", lang),
		))

		return false, nil

	case "start":

	}
	entry.Trace("unknown command")
	return true, nil
}

func (a *Admin) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}
