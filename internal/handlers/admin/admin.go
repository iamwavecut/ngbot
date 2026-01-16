package handlers

import (
	"context"
	"database/sql"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type Admin struct {
	s         bot.Service
	languages []string
}

func NewAdmin(s bot.Service) *Admin {
	entry := log.WithField("object", "Admin").WithField("method", "NewAdmin")

	a := &Admin{
		s:         s,
		languages: i18n.GetLanguagesList(),
	}
	a.startPanelCleanup()
	entry.Debug("created new admin handler")
	return a
}

func (a *Admin) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	entry := a.getLogEntry().WithField("method", "Handle")

	if u == nil {
		return true, nil
	}

	if u.MyChatMember != nil {
		if err := a.handleMyChatMember(ctx, u.MyChatMember); err != nil {
			entry.WithField("error", err.Error()).Error("failed to handle my_chat_member update")
			return false, err
		}
		return false, nil
	}

	if u.CallbackQuery != nil {
		handled, err := a.handlePanelCallback(ctx, u.CallbackQuery, chat, user)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to handle callback")
			return false, err
		}
		if handled {
			return false, nil
		}
		return true, nil
	}

	if u.Message == nil || user == nil || chat == nil {
		entry.Debug("chat or user is nil, proceeding")
		return true, nil
	}

	isAdmin, err := a.isAdmin(chat.ID, user.ID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("can't check admin status")
		return true, err
	}
	entry.Debugf("user is admin: %v", isAdmin)

	language := a.s.GetLanguage(ctx, chat.ID, user)

	if u.Message.IsCommand() {
		entry.Debugf("processing command: %s", u.Message.Command())
		switch u.Message.Command() {
		case "lang":
			return a.handleLangCommand(ctx, u.Message, isAdmin, language)
		case "settings":
			return false, a.handleSettingsCommand(ctx, u.Message, chat, user)
		case "start":
			payload := strings.TrimSpace(u.Message.CommandArguments())
			if strings.HasPrefix(payload, "settings_") {
				return false, a.handleStartSettings(ctx, u.Message, chat, user, payload)
			}
			return a.handleStartCommand(u.Message, language)
		default:
			entry.Debug("unknown command")
			return true, nil
		}
	}

	handled, err := a.handlePanelInput(ctx, u.Message, chat, user)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to handle panel input")
		return false, err
	}
	if handled {
		return false, nil
	}
	return true, nil
}

func (a *Admin) handleLangCommand(ctx context.Context, msg *api.Message, isAdmin bool, language string) (bool, error) {
	entry := a.getLogEntry().WithField("command", "lang")
	if !isAdmin {
		entry.Debug("user is not admin, ignoring command")
		return true, nil
	}

	argument := msg.CommandArguments()
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
			msg.Chat.ID,
			i18n.Get("You should use one of the following options", language)+": `"+strings.Join(a.languages, "`, `")+"`",
		)
		msg.ParseMode = api.ModeMarkdown
		msg.DisableNotification = true
		_, _ = a.s.GetBot().Send(msg)
		return false, nil
	}

	settings, err := a.s.GetDB().GetSettings(ctx, msg.Chat.ID)
	if tool.Try(err) {
		if errors.Cause(err) != sql.ErrNoRows {
			entry.WithField("error", err.Error()).Error("can't get chat settings")
			return true, errors.WithMessage(err, "cant get chat settings")
		}
	}

	settings.Language = argument
	err = a.s.GetDB().SetSettings(ctx, settings)
	if tool.Try(err) {
		entry.WithField("error", err.Error()).Error("can't update chat language")
		return false, errors.WithMessage(err, "cant update chat language")
	}

	entry.Debug("language set successfully")
	_, _ = a.s.GetBot().Send(api.NewMessage(
		msg.Chat.ID,
		i18n.Get("Language set successfully", language),
	))

	return false, nil
}

func (a *Admin) handleStartCommand(msg *api.Message, language string) (bool, error) {
	entry := a.getLogEntry().WithField("method", "handleStartCommand")
	entry.Debug("start command received")
	_, _ = a.s.GetBot().Send(api.NewMessage(
		msg.Chat.ID,
		i18n.Get("Bot started successfully", language),
	))

	return false, nil
}

func (a *Admin) isAdmin(chatID, userID int64) (bool, error) {
	b := a.s.GetBot()
	chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			UserID: userID,
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
		},
	})
	if err != nil {
		return false, errors.New("cant get chat member")
	}

	return chatMember.IsCreator() || (chatMember.IsAdministrator() && chatMember.CanRestrictMembers), nil
}

func (a *Admin) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}
