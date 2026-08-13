package handlers

import (
	"context"
	"fmt"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	log "github.com/sirupsen/logrus"
)

const logObjectBanlistGuard = "BanlistGuard"

type BanlistGuard struct {
	bot        *api.BotAPI
	store      banlistGuardStore
	banService moderation.BanService
}

type banlistGuardStore interface {
	IsChatNotSpammer(ctx context.Context, chatID int64, userID int64, username string) (bool, error)
}

type banlistedMessageOutcome struct {
	messageDeleted      bool
	userBanned          bool
	moderationAvailable bool
	err                 error
}

func NewBanlistGuard(botAPI *api.BotAPI, store banlistGuardStore, banService moderation.BanService) *BanlistGuard {
	return &BanlistGuard{bot: botAPI, store: store, banService: banService}
}

func (g *BanlistGuard) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	if u == nil {
		return true, nil
	}
	msg := u.Message
	if msg == nil {
		msg = u.EditedMessage
	}
	if msg == nil || msg.SenderChat != nil || len(msg.NewChatMembers) != 0 || chat == nil || user == nil || g.banService == nil {
		return true, nil
	}
	if !g.banService.IsKnownBanned(user.ID) {
		return true, nil
	}
	isNotSpammer, err := g.store.IsChatNotSpammer(ctx, chat.ID, user.ID, user.UserName)
	if err != nil {
		log.WithFields(log.Fields{
			logFieldObject: logObjectBanlistGuard,
			logFieldChatID: chat.ID,
			logFieldUserID: user.ID,
			logFieldError:  err.Error(),
		}).Error("failed to check manual not-spammer override; continuing banlist enforcement")
	} else if isNotSpammer {
		return true, nil
	}

	outcome := enforceBanlistedMessage(ctx, g.bot, g.banService, msg, chat, user)
	entry := log.WithFields(log.Fields{
		logFieldObject: logObjectBanlistGuard,
		logFieldChatID: chat.ID,
		logFieldUserID: user.ID,
		"message_id":   msg.MessageID,
		"edited":       u.EditedMessage != nil,
	})
	if outcome.err != nil {
		entry.WithField(logFieldError, outcome.err.Error()).Error("failed to enforce terminal banlist action")
		return false, outcome.err
	} else if !outcome.moderationAvailable {
		entry.Info("terminal banlist action skipped in no-rights mode")
	} else {
		entry.Info("terminal banlist action applied")
	}
	return false, nil
}

func enforceBanlistedMessage(
	ctx context.Context,
	botAPI *api.BotAPI,
	banService moderation.BanService,
	msg *api.Message,
	chat *api.Chat,
	user *api.User,
) banlistedMessageOutcome {
	if botAPI == nil || banService == nil || msg == nil || chat == nil || user == nil {
		return banlistedMessageOutcome{err: fmt.Errorf("banlist enforcement dependencies are incomplete")}
	}

	available, err := banService.ModerationAvailable(ctx, chat.ID)
	if err != nil {
		return banlistedMessageOutcome{err: bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)}
	}
	if !available {
		return banlistedMessageOutcome{}
	}

	outcome := banlistedMessageOutcome{moderationAvailable: true}
	if err := banService.BanUserWithMessage(ctx, chat.ID, user.ID, msg.MessageID); err != nil {
		outcome.err = fmt.Errorf("ban user: %w", err)
		return outcome
	}
	outcome.userBanned = true

	if err := bot.DeleteChatMessage(ctx, botAPI, chat.ID, msg.MessageID); err != nil && !isTelegramMessageAlreadyDeleted(err) {
		outcome.err = fmt.Errorf("delete message: %w", err)
		return outcome
	}
	outcome.messageDeleted = true
	return outcome
}
