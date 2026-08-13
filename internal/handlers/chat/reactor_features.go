package handlers

import (
	"context"

	api "github.com/OvyFlash/telegram-bot-api"
)

type ReactorFeatures struct {
	reactor *Reactor
}

func NewReactorFeatures(reactor *Reactor) *ReactorFeatures {
	return &ReactorFeatures{reactor: reactor}
}

func (f *ReactorFeatures) Handle(ctx context.Context, update *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	if f == nil || f.reactor == nil || update == nil || chat == nil {
		return true, nil
	}
	settings, err := f.reactor.getOrCreateSettings(ctx, chat)
	if err != nil {
		return false, err
	}
	ctx = context.WithValue(ctx, banlistPrecheckedContextKey{}, true)
	if update.CallbackQuery != nil {
		return f.reactor.handleCallbackQuery(ctx, update, chat, user)
	}
	if update.MessageReaction != nil {
		return f.reactor.handleMessageReaction(ctx, update.MessageReaction, chat, settings)
	}
	if update.Message == nil || user == nil {
		return true, nil
	}
	if update.Message.IsCommand() {
		return true, f.reactor.handleCommand(ctx, update.Message, chat, user, settings)
	}
	if messageMentionsCurrentBot(update.Message, f.reactor.bot.Self) {
		return true, f.reactor.voteBanCommand(ctx, update.Message, chat, user, settings)
	}
	return true, nil
}
