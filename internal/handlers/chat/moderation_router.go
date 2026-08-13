package handlers

import (
	"context"

	api "github.com/OvyFlash/telegram-bot-api"
)

type banlistPrecheckedContextKey struct{}

func banlistWasPrechecked(ctx context.Context) bool {
	checked, _ := ctx.Value(banlistPrecheckedContextKey{}).(bool)
	return checked
}

type ModerationRouter struct {
	banlist *BanlistGuard
	content *Reactor
}

func NewModerationRouter(banlist *BanlistGuard, content *Reactor) *ModerationRouter {
	return &ModerationRouter{banlist: banlist, content: content}
}

func (m *ModerationRouter) Handle(ctx context.Context, update *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	if m.banlist != nil {
		proceed, err := m.banlist.Handle(ctx, update, chat, user)
		if err != nil || !proceed {
			return proceed, err
		}
	}
	ctx = context.WithValue(ctx, banlistPrecheckedContextKey{}, true)
	if m.content == nil || update == nil || chat == nil {
		return true, nil
	}
	if update.Message == nil && update.EditedMessage == nil {
		return true, nil
	}
	settings, err := m.content.getOrCreateSettings(ctx, chat)
	if err != nil {
		return false, err
	}
	if update.EditedMessage != nil {
		if err := m.content.handleEditedMessage(ctx, update.EditedMessage, chat, user, settings); err != nil {
			return false, err
		}
		proceed := !m.content.messageWasModerated(chat.ID, update.EditedMessage.MessageID)
		return proceed, nil
	}
	if update.Message != nil {
		routed := user != nil && (update.Message.IsCommand() || messageMentionsCurrentBot(update.Message, m.content.bot.Self))
		if err := m.content.handleMessageChallenge(ctx, update.Message, chat, user, settings, false, routed); err != nil {
			return false, err
		}
		proceed := !m.content.messageWasModerated(chat.ID, update.Message.MessageID)
		return proceed, nil
	}
	return true, nil
}
