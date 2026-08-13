package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/pborman/uuid"
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

type moderationActionStore interface {
	BeginModerationAction(ctx context.Context, action *db.ModerationActionFence, owner string, now time.Time) (*db.ModerationActionFence, error)
	AdvanceModerationAction(ctx context.Context, actionKey, owner, expectedStatus, nextStatus, lastError string, now time.Time) (bool, error)
}

type deadlineBanService interface {
	BanUserWithMessageUntil(ctx context.Context, chatID, userID int64, messageID int, until time.Time) error
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

	var outcome banlistedMessageOutcome
	if actionStore, ok := g.store.(moderationActionStore); ok {
		outcome = g.enforceDurableBanlistedMessage(ctx, actionStore, u.UpdateID, msg, chat, user)
	} else {
		outcome = enforceBanlistedMessage(ctx, g.bot, g.banService, msg, chat, user)
	}
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

func (g *BanlistGuard) enforceDurableBanlistedMessage(ctx context.Context, store moderationActionStore, updateID int, msg *api.Message, chat *api.Chat, user *api.User) banlistedMessageOutcome {
	available, err := g.banService.ModerationAvailable(ctx, chat.ID)
	if err != nil {
		return banlistedMessageOutcome{err: bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)}
	}
	if !available {
		return banlistedMessageOutcome{}
	}
	now := time.Now()
	owner := uuid.New()
	actionKey := fmt.Sprintf("banlist:%d:%d:%d:%d", updateID, chat.ID, user.ID, msg.MessageID)
	action, err := store.BeginModerationAction(ctx, &db.ModerationActionFence{
		ActionKey: actionKey, ChatID: chat.ID, UserID: user.ID, MessageID: msg.MessageID, BanUntil: now.Add(10 * time.Minute),
	}, owner, now)
	if err != nil {
		return banlistedMessageOutcome{moderationAvailable: true, err: bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "moderation_fence_unavailable", err)}
	}
	outcome := banlistedMessageOutcome{moderationAvailable: true}
	switch action.Status {
	case db.ModerationActionCompleted:
		outcome.userBanned = true
		outcome.messageDeleted = true
		return outcome
	case db.ModerationActionReconciliation:
		outcome.err = bot.NewTerminalUpdateFailure(bot.UpdateFailureRuntime, "moderation_effect_ambiguous", errors.New("moderation action requires reconciliation"))
		return outcome
	case db.ModerationActionStarted:
		if action.Owner != owner {
			_, advanceErr := store.AdvanceModerationAction(ctx, action.ActionKey, action.Owner, db.ModerationActionStarted, db.ModerationActionReconciliation, "effect outcome unknown after restart", now)
			outcome.err = bot.NewTerminalUpdateFailure(bot.UpdateFailureRuntime, "moderation_effect_ambiguous", errors.Join(errors.New("moderation effect outcome unknown after restart"), advanceErr))
			return outcome
		}
		if service, ok := g.banService.(deadlineBanService); ok {
			err = service.BanUserWithMessageUntil(ctx, chat.ID, user.ID, msg.MessageID, action.BanUntil)
		} else {
			err = g.banService.BanUserWithMessage(ctx, chat.ID, user.ID, msg.MessageID)
		}
		if err != nil {
			if moderation.IsTelegramPrivilegeError(err) {
				g.banService.MarkModerationUnavailable(chat.ID)
				_, advanceErr := store.AdvanceModerationAction(ctx, action.ActionKey, owner, db.ModerationActionStarted, db.ModerationActionCompleted, "permission denied", time.Now())
				outcome.err = advanceErr
				outcome.moderationAvailable = false
				return outcome
			}
			_, advanceErr := store.AdvanceModerationAction(ctx, action.ActionKey, owner, db.ModerationActionStarted, db.ModerationActionReconciliation, db.SafeGatekeeperErrorCode(err), time.Now())
			outcome.err = bot.NewTerminalUpdateFailure(bot.UpdateFailureRuntime, "moderation_effect_ambiguous", errors.Join(err, advanceErr))
			return outcome
		}
		advanced, advanceErr := store.AdvanceModerationAction(ctx, action.ActionKey, owner, db.ModerationActionStarted, db.ModerationActionBanned, "", time.Now())
		if advanceErr != nil || !advanced {
			outcome.err = bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "persist_ban_effect", errors.Join(advanceErr, errors.New("ban effect fence changed")))
			return outcome
		}
		action.Status = db.ModerationActionBanned
		action.Owner = owner
		outcome.userBanned = true
	case db.ModerationActionBanned:
		outcome.userBanned = true
	default:
		outcome.err = bot.NewTerminalUpdateFailure(bot.UpdateFailurePayload, "invalid_moderation_action", fmt.Errorf("unexpected moderation action status %q", action.Status))
		return outcome
	}
	if err := bot.DeleteChatMessage(ctx, g.bot, chat.ID, msg.MessageID); err != nil && !isTelegramMessageAlreadyDeleted(err) {
		outcome.err = err
		return outcome
	}
	outcome.messageDeleted = true
	advanced, err := store.AdvanceModerationAction(ctx, action.ActionKey, action.Owner, db.ModerationActionBanned, db.ModerationActionCompleted, "", time.Now())
	if err != nil || !advanced {
		outcome.err = bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "complete_moderation_fence", errors.Join(err, errors.New("moderation completion fence changed")))
	}
	return outcome
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
