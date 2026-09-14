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

const (
	logObjectBanlistGuard         = "BanlistGuard"
	banlistActionPermissionDenied = "permission denied"
)

type BanlistGuard struct {
	bot        *api.BotAPI
	store      banlistGuardStore
	banService moderation.BanService
}

type banlistGuardStore interface {
	ResetMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) error
	DeleteMessageContext(ctx context.Context, chatID int64, messageID int) error
	IsChatNotSpammer(ctx context.Context, chatID int64, userID int64, username string) (bool, error)
}

type moderationActionStore interface {
	BeginModerationAction(ctx context.Context, action *db.ModerationActionFence, owner string, now time.Time) (*db.ModerationActionFence, error)
	MarkModerationActionEffectStarted(ctx context.Context, actionKey, owner string, now time.Time) (bool, error)
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
	proceed, _, err := g.handleWithPrecheck(ctx, u, chat, user)
	return proceed, err
}

func (g *BanlistGuard) handleWithPrecheck(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, moderation.BanlistPrecheck, error) {
	precheck := moderation.BanlistPrecheck{}
	if u == nil || chat == nil || g.banService == nil {
		return true, precheck, nil
	}
	user = moderationUpdateUser(u, user)
	if user == nil {
		return true, precheck, nil
	}
	precheck.ChatID = chat.ID
	precheck.UserID = user.ID
	precheck.Username = user.UserName
	msg := u.Message
	if msg == nil {
		msg = u.EditedMessage
	}
	if msg != nil && msg.SenderChat != nil {
		return true, precheck, nil
	}
	if msg != nil && len(msg.NewChatMembers) != 0 {
		proceed, err := g.handleJoinedMembers(ctx, u, msg, chat)
		return proceed, precheck, err
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
		precheck.AllowlistChecked = true
		precheck.Allowlisted = true
		return true, precheck, nil
	} else {
		precheck.AllowlistChecked = true
	}

	knownBanned := g.banService.IsKnownBanned(user.ID)
	available, err := g.banService.ModerationAvailable(ctx, chat.ID)
	if err != nil {
		log.WithFields(log.Fields{
			logFieldObject: logObjectBanlistGuard,
			logFieldChatID: chat.ID,
			logFieldUserID: user.ID,
			logFieldError:  err.Error(),
		}).Warn("moderation capability is unknown; stopping feature routing")
		return false, precheck, bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)
	}
	if !available {
		return !knownBanned, precheck, nil
	}

	banned := knownBanned
	if !banned {
		banned, err = g.banService.CheckBan(ctx, user.ID)
		if err != nil {
			return false, precheck, fmt.Errorf("check banlist before feature routing: %w", err)
		}
		precheck.ProviderChecked = true
	}
	if !banned {
		return true, precheck, nil
	}

	var outcome banlistedMessageOutcome
	if actionStore, ok := g.store.(moderationActionStore); ok {
		outcome = g.enforceDurableBanlistedMessage(ctx, actionStore, u.UpdateID, msg, chat, user)
	} else {
		outcome = g.enforce(ctx, msg, chat, user)
	}
	if outcome.userBanned {
		outcome.err = errors.Join(outcome.err, g.store.ResetMessageTrust(ctx, chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID}))
	}
	if outcome.messageDeleted && msg != nil {
		outcome.err = errors.Join(outcome.err, g.store.DeleteMessageContext(ctx, chat.ID, msg.MessageID))
	}
	entry := log.WithFields(log.Fields{
		logFieldObject: logObjectBanlistGuard,
		logFieldChatID: chat.ID,
		logFieldUserID: user.ID,
		"message_id":   messageID(msg),
		"edited":       u.EditedMessage != nil,
	})
	if outcome.err != nil {
		entry.WithField(logFieldError, outcome.err.Error()).Error("failed to enforce terminal banlist action")
		return false, precheck, outcome.err
	} else if !outcome.moderationAvailable {
		entry.Info("terminal banlist action skipped in no-rights mode")
	} else {
		entry.Info("terminal banlist action applied")
	}
	return false, precheck, nil
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
	msgID := messageID(msg)
	actionKey := fmt.Sprintf("banlist:%d:%d:%d:%d", updateID, chat.ID, user.ID, msgID)
	action, err := store.BeginModerationAction(ctx, &db.ModerationActionFence{
		ActionKey: actionKey, ChatID: chat.ID, UserID: user.ID, MessageID: msgID, BanUntil: now.Add(10 * time.Minute),
	}, owner, now)
	if err != nil {
		return banlistedMessageOutcome{moderationAvailable: true, err: bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "moderation_fence_unavailable", err)}
	}
	outcome := banlistedMessageOutcome{moderationAvailable: true}
	switch action.Status {
	case db.ModerationActionCompleted:
		if action.LastError == banlistActionPermissionDenied {
			return banlistedMessageOutcome{}
		}
		outcome.userBanned = true
		outcome.messageDeleted = action.MessageID != 0
		return outcome
	case db.ModerationActionReconciliation:
		outcome.err = bot.NewTerminalUpdateFailure(bot.UpdateFailureRuntime, "moderation_effect_ambiguous", errors.New("moderation action requires reconciliation"))
		return outcome
	case db.ModerationActionStarted:
		if action.Owner != owner {
			if !action.EffectStartedAt.Valid {
				outcome.err = bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "moderation_fence_claim_failed", errors.New("pre-effect moderation action ownership was not reclaimed"))
				return outcome
			}
			_, advanceErr := store.AdvanceModerationAction(ctx, action.ActionKey, action.Owner, db.ModerationActionStarted, db.ModerationActionReconciliation, "effect outcome unknown after restart", now)
			outcome.err = bot.NewTerminalUpdateFailure(bot.UpdateFailureRuntime, "moderation_effect_ambiguous", errors.Join(errors.New("moderation effect outcome unknown after restart"), advanceErr))
			return outcome
		}
		started, startErr := store.MarkModerationActionEffectStarted(ctx, action.ActionKey, owner, time.Now())
		if startErr != nil || !started {
			outcome.err = bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "mark_moderation_effect_started", errors.Join(startErr, errors.New("moderation effect start fence changed")))
			return outcome
		}
		if service, ok := g.banService.(deadlineBanService); ok {
			err = service.BanUserWithMessageUntil(ctx, chat.ID, user.ID, msgID, action.BanUntil)
		} else {
			err = g.banService.BanUserWithMessage(ctx, chat.ID, user.ID, msgID)
		}
		if err != nil {
			if moderation.IsTelegramPrivilegeError(err) {
				g.banService.MarkModerationUnavailable(chat.ID)
				_, advanceErr := store.AdvanceModerationAction(ctx, action.ActionKey, owner, db.ModerationActionStarted, db.ModerationActionCompleted, banlistActionPermissionDenied, time.Now())
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
	if msgID != 0 {
		if err := bot.DeleteChatMessageAndContext(ctx, g.bot, g.store, chat.ID, msgID); err != nil {
			outcome.err = err
			return outcome
		}
		outcome.messageDeleted = true
	}
	advanced, err := store.AdvanceModerationAction(ctx, action.ActionKey, action.Owner, db.ModerationActionBanned, db.ModerationActionCompleted, "", time.Now())
	if err != nil || !advanced {
		outcome.err = bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "complete_moderation_fence", errors.Join(err, errors.New("moderation completion fence changed")))
	}
	return outcome
}

func (g *BanlistGuard) handleJoinedMembers(ctx context.Context, u *api.Update, msg *api.Message, chat *api.Chat) (bool, error) {
	safeMembers := make([]api.User, 0, len(msg.NewChatMembers))
	for i := range msg.NewChatMembers {
		member := &msg.NewChatMembers[i]
		if member.IsBot {
			safeMembers = append(safeMembers, *member)
			continue
		}
		memberUpdate := *u
		memberMessage := *msg
		memberMessage.NewChatMembers = nil
		memberUpdate.Message = &memberMessage
		proceed, err := g.Handle(ctx, &memberUpdate, chat, member)
		if err != nil {
			return false, err
		}
		if proceed {
			safeMembers = append(safeMembers, *member)
		}
	}
	msg.NewChatMembers = safeMembers
	return len(safeMembers) != 0, nil
}

func moderationUpdateUser(u *api.Update, fallback *api.User) *api.User {
	if u.ChatMember != nil {
		if !isChatMemberJoinTransition(u.ChatMember) {
			return nil
		}
		return u.ChatMember.NewChatMember.User
	}
	if u.MyChatMember != nil {
		return nil
	}
	return fallback
}

func (g *BanlistGuard) enforce(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) banlistedMessageOutcome {
	if msg != nil {
		return enforceBanlistedMessage(ctx, g.bot, g.store, g.banService, msg, chat, user)
	}
	if err := g.banService.BanUserWithMessage(ctx, chat.ID, user.ID, 0); err != nil {
		return banlistedMessageOutcome{moderationAvailable: true, err: fmt.Errorf("ban user: %w", err)}
	}
	return banlistedMessageOutcome{moderationAvailable: true, userBanned: true}
}

func messageID(msg *api.Message) int {
	if msg == nil {
		return 0
	}
	return msg.MessageID
}

func enforceBanlistedMessage(
	ctx context.Context,
	botAPI *api.BotAPI,
	store banlistGuardStore,
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

	if err := bot.DeleteChatMessageAndContext(ctx, botAPI, store, chat.ID, msg.MessageID); err != nil {
		outcome.err = fmt.Errorf("delete message: %w", err)
		return outcome
	}
	outcome.messageDeleted = true
	return outcome
}
