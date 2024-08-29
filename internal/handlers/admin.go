package handlers

import (
	"context"
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
	entry := log.WithField("object", "Admin").WithField("method", "NewAdmin")
	entry.Debug("creating new admin handler")

	a := &Admin{
		s:         s,
		languages: i18n.GetLanguagesList(),
	}

	return a
}

func (a *Admin) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	entry := a.getLogEntry().WithField("method", "Handle")
	entry.Debug("handling update")

	if chat == nil || user == nil {
		entry.Debug("chat or user is nil, proceeding")
		return true, nil
	}

	b := a.s.GetBot()

	switch {
	case
		u.Message == nil,
		user.IsBot,
		!u.Message.IsCommand():
		entry.Debug("not a command or from a bot, proceeding")
		return true, nil
	}
	m := u.Message

	entry.Debugf("processing command: %s", m.Command())

	chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			UserID: user.ID,
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
		},
	})
	if err != nil {
		entry.WithError(err).Error("can't get chat member")
		return true, errors.New("cant get chat member")
	}
	var isAdmin bool
	switch {
	case
		chatMember.IsCreator(),
		chatMember.IsAdministrator() && chatMember.CanRestrictMembers:
		isAdmin = true
	}
	entry.Debugf("user is admin: %v", isAdmin)

	settings, err := a.s.GetDB().GetSettings(chat.ID)
	if tool.Try(err) {
		if errors.Cause(err) != sql.ErrNoRows {
			entry.WithError(err).Error("can't get chat settings")
			return true, errors.WithMessage(err, "cant get chat settings")
		}
	}
	if settings.Language == "" {
		settings.Language = config.Get().DefaultLanguage
	}
	entry.Debugf("chat settings: %+v", settings)

	switch m.Command() {
	case "lang":
		entry = entry.WithField("command", "lang")
		if !isAdmin {
			entry.Debug("user is not admin, ignoring command")
			break
		}

		argument := m.CommandArguments()
		entry.Debugf("language argument: %s", argument)

		isAllowed := false
		for _, allowedLanguage := range a.languages {
			if allowedLanguage == argument {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			entry.Debug("invalid language argument")
			msg := api.NewMessage(
				chat.ID,
				i18n.Get("You should use one of the following options", settings.Language)+": `"+strings.Join(a.languages, "`, `")+"`",
			)
			msg.ParseMode = api.ModeMarkdown
			msg.DisableNotification = true
			_, _ = b.Send(msg)
			return false, nil
		}

		settings.Language = argument
		err = a.s.GetDB().SetSettings(settings)
		if tool.Try(err) {
			entry.WithError(err).Error("can't update chat language")
			return false, errors.WithMessage(err, "cant update chat language")
		}

		entry.Debug("language set successfully")
		_, _ = b.Send(api.NewMessage(
			chat.ID,
			i18n.Get("Language set successfully", settings.Language),
		))

		return false, nil

	case "start":
		entry.Debug("start command received")

	default:
		entry.Debug("unknown command")
	}

	return true, nil
}

func (a *Admin) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}
