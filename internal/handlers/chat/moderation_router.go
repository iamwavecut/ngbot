package handlers

import (
	"context"
	"strconv"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

type banlistPrecheckedContextKey struct{}

func banlistWasPrechecked(ctx context.Context) bool {
	checked, _ := ctx.Value(banlistPrecheckedContextKey{}).(bool)
	return checked
}

type ModerationRouter struct {
	banlist  *BanlistGuard
	content  *Reactor
	features *ReactorFeatures
}

func NewModerationRouter(banlist *BanlistGuard, content *Reactor, features ...*ReactorFeatures) *ModerationRouter {
	router := &ModerationRouter{banlist: banlist, content: content}
	if len(features) != 0 {
		router.features = features[0]
	}
	return router
}

func (m *ModerationRouter) Handle(ctx context.Context, update *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	moderationChat := chat
	if m.features != nil && m.features.reactor != nil && m.features.reactor.spamControl != nil {
		caseID, ok := spamVoteCaseID(update)
		if ok {
			targetChatID, found, err := m.features.reactor.spamControl.VoteTargetChat(ctx, caseID)
			if err != nil {
				return false, err
			}
			if found {
				moderationChat = &api.Chat{ID: targetChatID}
			}
		}
	}
	if m.banlist != nil {
		proceed, precheck, err := m.banlist.handleWithPrecheck(ctx, update, moderationChat, user)
		if err != nil || !proceed {
			return proceed, err
		}
		ctx = moderation.WithBanlistPrecheck(ctx, precheck)
	}
	ctx = context.WithValue(ctx, banlistPrecheckedContextKey{}, true)
	if m.features != nil && isSpamVoteCallback(update) {
		_, err := m.features.Handle(ctx, update, chat, user)
		return false, err
	}
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

func isSpamVoteCallback(update *api.Update) bool {
	return update != nil && update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, "spam_vote:")
}

func spamVoteCaseID(update *api.Update) (int64, bool) {
	if !isSpamVoteCallback(update) {
		return 0, false
	}
	parts := strings.Split(update.CallbackQuery.Data, ":")
	if len(parts) != 3 {
		return 0, false
	}
	caseID, err := strconv.ParseInt(parts[1], 10, 64)
	return caseID, err == nil
}
