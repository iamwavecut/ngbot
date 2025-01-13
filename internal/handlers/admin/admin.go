package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type Admin struct {
	s           bot.Service
	languages   []string
	banService  moderation.BanService
	spamControl *moderation.SpamControl
}

func NewAdmin(s bot.Service, banService moderation.BanService, spamControl *moderation.SpamControl) *Admin {
	entry := log.WithField("object", "Admin").WithField("method", "NewAdmin")

	a := &Admin{
		s:           s,
		banService:  banService,
		spamControl: spamControl,
		languages:   i18n.GetLanguagesList(),
	}
	entry.Debug("created new admin handler")
	return a
}

func (a *Admin) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	entry := a.getLogEntry().WithField("method", "Handle")

	if chat == nil || user == nil {
		entry.Debug("chat or user is nil, proceeding")
		return true, nil
	}

	switch {
	case
		u.Message == nil,
		user.IsBot,
		!u.Message.IsCommand():
		return true, nil
	}
	m := u.Message

	entry.Debugf("processing command: %s", m.Command())

	isAdmin, err := a.isAdmin(chat.ID, user.ID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("can't check admin status")
		return true, err
	}
	entry.Debugf("user is admin: %v", isAdmin)

	language := a.s.GetLanguage(ctx, chat.ID, user)

	switch m.Command() {
	case "lang":
		return a.handleLangCommand(ctx, m, isAdmin, language)
	case "start":
		return a.handleStartCommand(m, language)

	// case "ban":
	// 	return a.handleBanCommand(ctx, m, isAdmin, language)

	default:
		entry.Debug("unknown command")
		return true, nil
	}
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

func (a *Admin) handleBanCommand(ctx context.Context, msg *api.Message, isAdmin bool, language string) (bool, error) {
	entry := log.WithField("method", "handleBanCommand")
	entry.Debug("handling ban command")

	if msg.Chat.Type == "private" {
		entry.Debug("command used in private chat, ignoring")
		msg := api.NewMessage(msg.Chat.ID, i18n.Get("This command can only be used in groups", language))
		msg.DisableNotification = true
		_, _ = a.s.GetBot().Send(msg)
		return false, nil
	}

	if msg.ReplyToMessage == nil {
		msg := api.NewMessage(msg.Chat.ID, i18n.Get("This command must be used as a reply to a message", language))
		msg.DisableNotification = true
		_, _ = a.s.GetBot().Send(msg)
		return false, nil
	}

	if isAdmin {
		return false, a.handleAdminBan(ctx, msg, language)
	} else {
		return false, a.handleUserBanVote(ctx, msg, language)
	}
}

func (a *Admin) handleAdminBan(ctx context.Context, msg *api.Message, language string) error {
	targetMsg := msg.ReplyToMessage

	err := bot.BanUserFromChat(ctx, a.s.GetBot(), targetMsg.From.ID, msg.Chat.ID)
	if err != nil {
		if errors.Is(err, moderation.ErrNoPrivileges) {
			msg := api.NewMessage(msg.Chat.ID, i18n.Get("I don't have enough rights to ban this user", language))
			msg.DisableNotification = true
			_, _ = a.s.GetBot().Send(msg)
		}
		return fmt.Errorf("failed to ban user: %w", err)
	}

	err = bot.DeleteChatMessage(ctx, a.s.GetBot(), msg.Chat.ID, targetMsg.MessageID)
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}

	err = bot.DeleteChatMessage(ctx, a.s.GetBot(), msg.Chat.ID, msg.MessageID)
	if err != nil {
		return fmt.Errorf("failed to delete command message: %w", err)
	}

	return nil
}

func (a *Admin) handleUserBanVote(ctx context.Context, msg *api.Message, language string) error {
	targetMsg := msg.ReplyToMessage
	if targetMsg == nil {
		err := bot.DeleteChatMessage(ctx, a.s.GetBot(), msg.Chat.ID, msg.MessageID)
		if err != nil {
			return fmt.Errorf("failed to delete command message: %w", err)
		}
	}
	return a.spamControl.ProcessSuspectMessage(ctx, targetMsg, language)
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
