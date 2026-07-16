package handlers

import (
	"context"
	"fmt"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	log "github.com/sirupsen/logrus"
)

type BanlistGuard struct {
	bot        *api.BotAPI
	banService moderation.BanService
}

type banlistedMessageOutcome struct {
	messageDeleted      bool
	userBanned          bool
	moderationAvailable bool
	err                 error
}

func NewBanlistGuard(botAPI *api.BotAPI, banService moderation.BanService) *BanlistGuard {
	return &BanlistGuard{bot: botAPI, banService: banService}
}

func (g *BanlistGuard) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	if u == nil || u.Message == nil || u.Message.SenderChat != nil || len(u.Message.NewChatMembers) != 0 || chat == nil || user == nil || g.banService == nil {
		return true, nil
	}
	if !g.banService.IsKnownBanned(user.ID) {
		return true, nil
	}

	outcome := enforceBanlistedMessage(ctx, g.bot, g.banService, u.Message, chat, user)
	entry := log.WithFields(log.Fields{
		"object":       "BanlistGuard",
		logFieldChatID: chat.ID,
		logFieldUserID: user.ID,
		"message_id":   u.Message.MessageID,
	})
	if outcome.err != nil {
		entry.WithField(logFieldError, outcome.err.Error()).Error("failed to enforce terminal banlist action")
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
		return banlistedMessageOutcome{err: fmt.Errorf("inspect moderation rights: %w", err)}
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

	if err := bot.DeleteChatMessage(ctx, botAPI, chat.ID, msg.MessageID); err != nil && !isTelegramActionAlreadyApplied(err) {
		outcome.err = fmt.Errorf("delete message: %w", err)
		return outcome
	}
	outcome.messageDeleted = true
	return outcome
}
