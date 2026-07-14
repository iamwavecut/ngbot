package handlers

import (
	"context"
	stderrors "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const maxChallengeActionAttempts = 8

const (
	approvedJoinRequestChallengeTTL = 5 * time.Minute
	webAppOpenDeadline              = 11 * time.Second
)

func (g *Gatekeeper) handleChallenge(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (err error) {
	entry := g.getLogEntry().WithField(logFieldMethod, "handleChallenge")
	entry.Debug("handling challenge")

	if u == nil || u.CallbackQuery == nil || chat == nil || user == nil {
		entry.Debug("missing callback context")
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	b := g.bot
	cq := u.CallbackQuery

	entry.WithFields(log.Fields{
		"data":       cq.Data,
		logFieldUser: bot.GetUN(user),
		logFieldChat: chat.ID,
	}).Debug("callback query data")

	joinerID, challengeUUID := func(s string) (int64, string) {
		entry := g.getLogEntry().WithField(logFieldMethod, "handleChallenge.parseCallbackData")
		entry.WithField("data", s).Debug("parsing callback data")
		parts := strings.Split(s, ";")
		if len(parts) != 2 {
			return 0, ""
		}
		ID, err := strconv.ParseInt(parts[0], 10, 0)
		if err != nil {
			return 0, ""
		}
		entry.WithFields(log.Fields{"joinerID": ID, "challengeUUID": parts[1]}).Debug("parsed callback data")
		return ID, parts[1]
	}(cq.Data)
	if joinerID == 0 || challengeUUID == "" {
		return nil
	}

	if user.ID != joinerID {
		language := g.s.GetLanguage(ctx, chat.ID, user)
		if _, err := b.RequestWithContext(ctx, api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", language))); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
		}
		return nil
	}

	messageID := 0
	if cq.Message != nil {
		messageID = cq.Message.MessageID
	}
	challenge, err := g.store.GetChallengeByMessage(ctx, chat.ID, joinerID, messageID)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to fetch challenge")
		return err
	}
	if challenge == nil {
		entry.Debug("no user matched for challenge")
		if _, err := b.RequestWithContext(ctx, api.NewCallback(cq.ID, i18n.Get("This challenge isn't your concern", g.s.GetLanguage(ctx, chat.ID, user)))); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
		}
		return nil
	}

	targetChat, err := bot.GetChat(ctx, g.bot, api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: challenge.ChatID,
		},
	})
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("cant get target chat info")
		return errors.WithMessage(err, "cant get target chat info")
	}

	targetSettings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to fetch target settings")
		return err
	}
	if !targetSettings.GatekeeperEnabled || !targetSettings.GatekeeperCaptchaEnabled {
		if _, err := b.RequestWithContext(ctx, api.NewCallback(cq.ID, i18n.Get("Gatekeeper is disabled for this chat", g.s.GetLanguage(ctx, challenge.ChatID, user)))); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
		}
		return nil
	}

	language := g.s.GetLanguage(ctx, targetChat.ID, user)
	if challenge.CommChatID != challenge.ChatID {
		language = g.dmLanguage(challenge.UserLanguage, user)
	}
	rejectDuration, rejectText, err := g.rejectConfigFromSettings(targetSettings, language, targetChat.Title)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to build reject config")
		return err
	}

	if time.Now().After(challenge.ExpiresAt) {
		if _, err := b.RequestWithContext(ctx, api.NewCallbackWithAlert(cq.ID, rejectText)); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
		}
		return g.failChallenge(ctx, challenge, rejectText, rejectDuration)
	}

	if challenge.SuccessUUID != challengeUUID {
		if _, err := b.RequestWithContext(ctx, api.NewCallbackWithAlert(cq.ID, rejectText)); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
		}
		attempts, status, updated, err := g.store.RecordWrongAttempt(ctx, challenge.ChallengeID, maxChallengeAttempts)
		if err != nil {
			return err
		}
		if !updated {
			return nil
		}
		challenge.Attempts = attempts
		challenge.Status = status
		if status == db.ChallengeStatusRejectPending {
			return g.processChallengeAction(ctx, challenge)
		}
		return nil
	}

	if _, err := b.RequestWithContext(ctx, api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", language))); err != nil {
		entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
	}

	return g.completeChallenge(ctx, challenge, &targetChat, language)
}

func (g *Gatekeeper) completeChallenge(ctx context.Context, challenge *db.Challenge, target *api.ChatFullInfo, language string) error {
	_ = target
	_ = language
	nextStatus := db.ChallengeStatusUnrestrictPending
	if challenge.CommChatID != challenge.ChatID {
		nextStatus = db.ChallengeStatusApproveMemberPending
	}
	claimed, err := g.store.CompleteExternalAction(ctx, challenge.ChallengeID, db.ChallengeStatusPending, nextStatus, time.Time{})
	if err != nil || !claimed {
		return err
	}
	challenge.Status = nextStatus
	if err := g.processChallengeAction(ctx, challenge); err != nil {
		return err
	}
	if challenge.CommChatID != challenge.ChatID && target != nil {
		msg := api.NewMessage(
			challenge.CommChatID,
			fmt.Sprintf(
				i18n.Get("Awesome, you're good to go! Feel free to start chatting in the group \"%s\".", language),
				api.EscapeText(api.ModeMarkdown, target.Title),
			),
		)
		msg.ParseMode = api.ModeMarkdown
		_ = tool.Err(bot.Send(ctx, g.bot, msg))
	}
	return nil
}

func (g *Gatekeeper) failChallenge(ctx context.Context, challenge *db.Challenge, rejectText string, rejectDuration time.Duration) error {
	_ = rejectText
	_ = rejectDuration
	if challenge.Status != db.ChallengeStatusRejectPending {
		claimed, err := g.store.CompleteExternalAction(ctx, challenge.ChallengeID, challenge.Status, db.ChallengeStatusRejectPending, time.Time{})
		if err != nil || !claimed {
			return err
		}
		challenge.Status = db.ChallengeStatusRejectPending
	}
	return g.processChallengeAction(ctx, challenge)
}

func (g *Gatekeeper) cleanupChallengeWithoutPenalty(ctx context.Context, challenge *db.Challenge) error {
	entry := g.getLogEntry().WithField(logFieldMethod, "cleanupChallengeWithoutPenalty")
	b := g.bot

	if challenge.ChallengeMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, b, challenge.CommChatID, challenge.ChallengeMessageID); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("cant delete challenge message")
		}
	}

	if challenge.CommChatID == challenge.ChatID {
		claimed, err := g.store.CompleteExternalAction(ctx, challenge.ChallengeID, challenge.Status, db.ChallengeStatusUnrestrictPending, time.Time{})
		if err != nil || !claimed {
			return err
		}
		challenge.Status = db.ChallengeStatusUnrestrictPending
		return g.processChallengeActionWithoutStats(ctx, challenge)
	}
	_, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, challenge.Status)
	return err
}

func (g *Gatekeeper) processChallengeAction(ctx context.Context, challenge *db.Challenge) error {
	return g.processChallengeActionWithStats(ctx, challenge, true)
}

func (g *Gatekeeper) processChallengeActionWithoutStats(ctx context.Context, challenge *db.Challenge) error {
	return g.processChallengeActionWithStats(ctx, challenge, false)
}

func (g *Gatekeeper) processChallengeActionWithStats(ctx context.Context, challenge *db.Challenge, recordStats bool) error {
	if challenge == nil {
		return nil
	}
	entry := g.getLogEntry().WithFields(log.Fields{
		logFieldMethod: "processChallengeAction",
		"challenge_id": challenge.ChallengeID,
		logFieldStatus: challenge.Status,
	})

	var actionErr error
	switch challenge.Status {
	case db.ChallengeStatusWebAppFallbackPending:
		settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if err != nil {
			actionErr = err
		} else {
			actionErr = g.fallbackClaimedWebAppChallenge(ctx, challenge, settings)
		}
		if actionErr == nil {
			return nil
		}
	case db.ChallengeStatusApproveQueryPending:
		actionErr = bot.AnswerJoinRequestQuery(ctx, g.bot, challenge.JoinRequestQueryID, bot.JoinRequestQueryResultApprove)
		if actionErr == nil || isTelegramActionAlreadyApplied(actionErr) {
			g.deleteChallengePrompt(ctx, challenge)
			changed, err := g.store.CompleteExternalAction(ctx, challenge.ChallengeID, challenge.Status, db.ChallengeStatusPassedWaitingMemberJoin, time.Now().Add(approvedJoinRequestChallengeTTL))
			if err != nil {
				return err
			}
			if changed && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
			}
			return nil
		}
	case db.ChallengeStatusApproveMemberPending:
		actionErr = bot.ApproveJoinRequest(ctx, g.bot, challenge.UserID, challenge.ChatID)
		if actionErr == nil || isTelegramActionAlreadyApplied(actionErr) {
			g.deleteChallengePrompt(ctx, challenge)
			changed, err := g.store.CompleteExternalAction(ctx, challenge.ChallengeID, challenge.Status, db.ChallengeStatusPassedWaitingMemberJoin, time.Now().Add(approvedJoinRequestChallengeTTL))
			if err != nil {
				return err
			}
			if changed && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
			}
			return nil
		}
	case db.ChallengeStatusUnrestrictPending:
		actionErr = bot.UnrestrictChatting(ctx, g.bot, challenge.UserID, challenge.ChatID)
		if actionErr == nil || isTelegramActionAlreadyApplied(actionErr) {
			g.deleteChallengePrompt(ctx, challenge)
			deleted, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, challenge.Status)
			if err != nil {
				return err
			}
			if deleted && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
			}
			return nil
		}
	case db.ChallengeStatusRejectPending:
		if challenge.CommChatID != challenge.ChatID {
			currentMember, err := g.isCurrentJoinRequestMember(ctx, challenge)
			if err != nil {
				actionErr = err
				break
			}
			if currentMember {
				return g.cleanupChallengeWithoutPenalty(ctx, challenge)
			}
		}
		settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if err != nil {
			actionErr = err
			break
		}
		banErr := bot.BanUserFromChat(ctx, g.bot, challenge.UserID, challenge.ChatID, time.Now().Add(settings.GetRejectTimeout()).Unix())
		if isTelegramActionAlreadyApplied(banErr) {
			banErr = nil
		}
		var declineErr error
		if challenge.JoinRequestQueryID != "" {
			declineErr = bot.AnswerJoinRequestQuery(ctx, g.bot, challenge.JoinRequestQueryID, bot.JoinRequestQueryResultDecline)
		} else if challenge.CommChatID != challenge.ChatID {
			declineErr = bot.DeclineJoinRequest(ctx, g.bot, challenge.UserID, challenge.ChatID)
		}
		if isTelegramActionAlreadyApplied(declineErr) {
			declineErr = nil
		}
		actionErr = stderrors.Join(banErr, declineErr)
		if actionErr == nil {
			g.deleteChallengeMessages(ctx, challenge)
			deleted, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, challenge.Status)
			if err != nil {
				return err
			}
			if deleted && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengeFailed)
			}
			return nil
		}
	default:
		return nil
	}

	if actionErr == nil {
		return nil
	}
	nextAttemptAt := time.Time{}
	if challenge.AttemptCount+1 < maxChallengeActionAttempts {
		nextAttemptAt = time.Now().Add(challengeRetryDelay(challenge.AttemptCount))
	}
	scheduled, scheduleErr := g.store.ScheduleChallengeRetry(ctx, challenge.ChallengeID, challenge.Status, nextAttemptAt, actionErr.Error())
	if scheduleErr != nil {
		return stderrors.Join(actionErr, scheduleErr)
	}
	if scheduled {
		fields := log.Fields{logFieldError: actionErr.Error(), "attempt": challenge.AttemptCount + 1}
		if nextAttemptAt.IsZero() {
			entry.WithFields(fields).Error("gatekeeper action retries exhausted; durable state retained")
			return actionErr
		}
		entry.WithFields(fields).WithField("retry_in", time.Until(nextAttemptAt)).Warn("gatekeeper action failed; retry scheduled")
	}
	return actionErr
}

func (g *Gatekeeper) deleteChallengeMessages(ctx context.Context, challenge *db.Challenge) {
	g.deleteChallengePrompt(ctx, challenge)
	entry := g.getLogEntry().WithField("challenge_id", challenge.ChallengeID)
	if challenge.JoinMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, g.bot, challenge.ChatID, challenge.JoinMessageID); err != nil && !isTelegramActionAlreadyApplied(err) {
			entry.WithField(logFieldError, err.Error()).Warn("failed to delete join message")
		}
	}
}

func (g *Gatekeeper) deleteChallengePrompt(ctx context.Context, challenge *db.Challenge) {
	entry := g.getLogEntry().WithField("challenge_id", challenge.ChallengeID)
	if challenge.ChallengeMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, g.bot, challenge.CommChatID, challenge.ChallengeMessageID); err != nil && !isTelegramActionAlreadyApplied(err) {
			entry.WithField(logFieldError, err.Error()).Warn("failed to delete challenge message")
		}
	}
}

func (g *Gatekeeper) incrementChallengeStat(ctx context.Context, chatID int64, stat string) {
	if err := handlersbase.IncrementDailyStat(ctx, g.stats, chatID, stat); err != nil {
		g.getLogEntry().WithField(logFieldError, err.Error()).Warn("failed to increment challenge stat")
	}
}

func challengeRetryDelay(attempt int) time.Duration {
	return min(5*time.Second*time.Duration(1<<min(attempt, 8)), 15*time.Minute)
}

func isTelegramActionAlreadyApplied(err error) bool {
	if err == nil {
		return true
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range []string{
		"USER_ALREADY_PARTICIPANT",
		"HIDE_REQUESTER_MISSING",
		"QUERY IS TOO OLD",
		"QUERY ID IS INVALID",
		"QUERY_ID_INVALID",
		"MESSAGE TO DELETE NOT FOUND",
		"MESSAGE_ID_INVALID",
		"USER_NOT_PARTICIPANT",
		"PARTICIPANT_ID_INVALID",
		"MEMBER NOT FOUND",
		"USER IS DEACTIVATED",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (g *Gatekeeper) isCurrentJoinRequestMember(ctx context.Context, challenge *db.Challenge) (bool, error) {
	member, err := bot.GetChatMember(ctx, g.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: challenge.ChatID},
			UserID:     challenge.UserID,
		},
	})
	if err != nil {
		if isTelegramActionAlreadyApplied(err) {
			return false, nil
		}
		return false, fmt.Errorf("check join-request membership before rejection: %w", err)
	}
	return isCurrentChatMember(member), nil
}

func (g *Gatekeeper) rejectConfigFromSettings(settings *db.Settings, language string, title string) (time.Duration, string, error) {
	if settings == nil {
		return 0, "", errors.New("settings are nil")
	}
	rejectDuration := settings.GetRejectTimeout()
	rejectMinutes := max(int(rejectDuration.Minutes()), 1)
	rejectText := fmt.Sprintf(
		i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", language),
		title,
		strconv.Itoa(rejectMinutes),
	)
	return rejectDuration, rejectText, nil
}
