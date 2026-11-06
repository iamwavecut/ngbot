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
	joinDeclineEffect = "banlist-join"
	joinBanEffect     = "banlist-join-ban"
)

type banlistedJoinStore interface {
	ClaimBanlistedJoinEffect(ctx context.Context, effect *db.ModerationActionFence, now, reclaimBefore time.Time) (*db.ModerationActionFence, error)
	AdvanceModerationAction(ctx context.Context, actionKey, owner, expectedStatus, nextStatus, lastError string, now time.Time) (bool, error)
	DeleteAuthorMessageContext(ctx context.Context, chatID int64, author db.MessageAuthor) error
	ResetMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) error
}

func enforceBanlistedJoin(ctx context.Context, botAPI *api.BotAPI, store banlistedJoinStore, checker GatekeeperBanChecker, eventID string, request *api.ChatJoinRequest) error {
	suffix := fmt.Sprintf("%s:%d:%d", eventID, request.Chat.ID, request.From.ID)
	declineErr := applyBanlistedJoinEffect(ctx, botAPI, store, request, suffix, joinDeclineEffect)
	banErr := applyBanlistedJoinEffect(ctx, botAPI, store, request, suffix, joinBanEffect)
	for _, err := range []error{declineErr, banErr} {
		if err != nil && bot.ClassifyUpdateFailure(err).Disposition == bot.UpdateFailureRetryable {
			return err
		}
	}
	if banErr != nil && bot.ClassifyUpdateFailure(banErr).Reason == db.GatekeeperErrorPermission {
		checker.MarkModerationUnavailable(request.Chat.ID)
	}
	return errors.Join(declineErr, banErr)
}

func applyBanlistedJoinEffect(ctx context.Context, botAPI *api.BotAPI, store banlistedJoinStore, request *api.ChatJoinRequest, suffix, kind string) error {
	now := time.Now().UTC()
	owner := uuid.New()
	chatID, userID := request.Chat.ID, request.From.ID
	action, err := store.ClaimBanlistedJoinEffect(ctx, &db.ModerationActionFence{
		ActionKey: kind + ":" + suffix, Owner: owner, ChatID: chatID, UserID: userID,
	}, now, now.Add(-2*joinQueryResponseTimeout))
	if err != nil {
		return err
	}
	advance := func(next, code string) error {
		ok, err := store.AdvanceModerationAction(ctx, action.ActionKey, action.Owner, action.Status, next, code, time.Now().UTC())
		if err != nil || !ok {
			return bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "join_effect_checkpoint", errors.Join(err, errors.New("join effect checkpoint changed")))
		}
		action.Status = next
		return nil
	}
	switch action.Status {
	case db.ModerationActionCompleted:
		if action.LastError == "" {
			return nil
		}
		fallthrough
	case db.ModerationActionReconciliation:
		return bot.NewTerminalUpdateFailure(bot.UpdateFailureTelegram, db.SafeGatekeeperErrorText(action.LastError), errors.New(action.LastError))
	case db.ModerationActionStarted:
		if action.Owner != owner {
			return bot.NewRetryableUpdateFailure(bot.UpdateFailureSQLite, "join_effect_busy", errors.New("join effect already in progress"))
		}
		effectCtx, cancel := context.WithTimeout(ctx, joinQueryResponseTimeout)
		if kind == joinDeclineEffect {
			err = declineBanlistedApplicant(effectCtx, botAPI, request)
		} else {
			err = banJoinApplicant(effectCtx, botAPI, chatID, userID, !action.CreatedAt.Equal(now))
		}
		cancel()
		entry := log.WithFields(log.Fields{logFieldObject: logObjectBanlistGuard, logFieldChatID: chatID, logFieldUserID: userID, "effect": kind})
		if err != nil {
			failure := bot.ClassifyUpdateFailure(err)
			code := db.SafeGatekeeperErrorCode(err)
			if moderation.IsTelegramPrivilegeError(err) {
				failure.Disposition = bot.UpdateFailureTerminal
				code = db.GatekeeperErrorPermission
			}
			next := db.ModerationActionPending
			if failure.Disposition == bot.UpdateFailureTerminal {
				next = db.ModerationActionReconciliation
			}
			entry.WithField(logFieldErrorCode, code).Warn("banlisted join action failed")
			if persistErr := advance(next, code); persistErr != nil {
				return persistErr
			}
			if next == db.ModerationActionReconciliation {
				return bot.NewTerminalUpdateFailure(failure.Source, code, errors.New(code))
			}
			return bot.NewRetryableUpdateFailure(failure.Source, failure.Reason, errors.New(code))
		}
		next := db.ModerationActionCompleted
		if kind == joinBanEffect {
			next = db.ModerationActionBanned
		}
		if err := advance(next, ""); err != nil {
			return err
		}
		entry.Info("banlisted join action confirmed")
	case db.ModerationActionBanned:
	default:
		return bot.NewTerminalUpdateFailure(bot.UpdateFailureRuntime, "invalid_join_effect", fmt.Errorf("unknown join effect status %q", action.Status))
	}
	if action.Status == db.ModerationActionBanned {
		author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: userID}
		if err := store.DeleteAuthorMessageContext(ctx, chatID, author); err != nil {
			return err
		}
		if err := store.ResetMessageTrust(ctx, chatID, author); err != nil {
			return err
		}
		return advance(db.ModerationActionCompleted, "")
	}
	return nil
}

func declineBanlistedApplicant(ctx context.Context, botAPI *api.BotAPI, request *api.ChatJoinRequest) error {
	if request.QueryID != "" {
		err := bot.AnswerJoinRequestQuery(ctx, botAPI, request.QueryID, bot.JoinRequestQueryResultDecline)
		if err == nil || db.SafeGatekeeperErrorCode(err) != db.GatekeeperErrorQueryExpired {
			return err
		}
	}
	err := bot.DeclineJoinRequest(ctx, botAPI, request.From.ID, request.Chat.ID)
	if isTelegramJoinDeclineAlreadyApplied(err) {
		return nil
	}
	return err
}

func banJoinApplicant(ctx context.Context, botAPI *api.BotAPI, chatID, userID int64, retry bool) error {
	if retry {
		member, err := bot.GetChatMember(ctx, botAPI, api.NewGetChatMember(chatID, userID))
		if err != nil && !isTelegramRemovalAlreadyApplied(err) {
			return err
		}
		if err == nil && member.WasKicked() && member.UntilDate == 0 {
			return nil
		}
	}
	return bot.BanUserFromChat(ctx, botAPI, userID, chatID, 0)
}
