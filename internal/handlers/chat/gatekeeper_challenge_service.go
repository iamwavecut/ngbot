package handlers

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
	"github.com/pborman/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const maxChallengeActionAttempts = 8

const (
	approvedJoinRequestChallengeTTL = 5 * time.Minute
	webAppOpenDeadline              = 11 * time.Second
	noPrivilegesNoticeRetention     = 30 * time.Minute
	challengeActionLeaseDuration    = 2 * time.Minute
	challengeActionTimeout          = 90 * time.Second
	joinQueryResponseTimeout        = 8 * time.Second
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
	banned, err := g.revalidateChallengeIdentity(ctx, challenge, user.UserName)
	if err != nil {
		return err
	}
	if banned {
		return g.failChallenge(ctx, challenge, rejectText, rejectDuration)
	}

	if _, err := b.RequestWithContext(ctx, api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", language))); err != nil {
		entry.WithField(logFieldError, err.Error()).Error("cant answer callback query")
	}

	return g.completeChallenge(ctx, challenge, &targetChat, language)
}

func (g *Gatekeeper) revalidateChallengeIdentity(ctx context.Context, challenge *db.Challenge, username string) (bool, error) {
	if challenge == nil {
		return false, errors.New("challenge is nil")
	}
	isNotSpammer, err := g.store.IsChatNotSpammer(ctx, challenge.ChatID, challenge.UserID, username)
	if err == nil && isNotSpammer {
		return false, nil
	}
	if err != nil {
		g.getLogEntry().WithFields(log.Fields{
			logFieldUserID: challenge.UserID,
			logFieldError:  err.Error(),
		}).Error("failed to check manual not-spammer override; continuing moderation")
	}
	if g.banChecker == nil {
		return false, nil
	}
	if g.banChecker.IsKnownBanned(challenge.UserID) {
		return true, nil
	}
	banned, err := g.banChecker.CheckBan(ctx, challenge.UserID)
	if err != nil {
		return false, fmt.Errorf("recheck challenge banlist: %w", err)
	}
	return banned, nil
}

func (g *Gatekeeper) completeChallenge(ctx context.Context, challenge *db.Challenge, target *api.ChatFullInfo, language string) error {
	_ = target
	_ = language
	if challenge.CommChatID == challenge.ChatID && !challenge.UserRestricted {
		g.deleteChallengePrompt(ctx, challenge)
		deleted, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, db.ChallengeStatusPending)
		if deleted {
			g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
		}
		return err
	}
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

	if challenge.CommChatID == challenge.ChatID && challenge.UserRestricted {
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

func (g *Gatekeeper) beginChallengeEffect(ctx context.Context, challenge *db.Challenge, owner, phase string) error {
	version, changed, err := g.store.BeginLeasedChallengeEffect(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, phase, time.Now())
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("challenge effect fence was lost")
	}
	challenge.ActionVersion = version
	challenge.ActionPhase = phase
	return nil
}

func (g *Gatekeeper) advanceChallengePhase(ctx context.Context, challenge *db.Challenge, owner, phase string) error {
	version, changed, err := g.store.AdvanceLeasedChallengePhase(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, phase, time.Now())
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("challenge phase fence was lost")
	}
	challenge.ActionVersion = version
	challenge.ActionPhase = phase
	return nil
}

func (g *Gatekeeper) reconcileAmbiguousChallengeEffect(ctx context.Context, challenge *db.Challenge, owner string, artifactMessageID int, cause error) error {
	safeCause := safeGatekeeperError(cause)
	reconciled, err := g.store.ReconcileLeasedChallengeVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, artifactMessageID, db.SafeGatekeeperErrorCode(cause), time.Now())
	if err != nil {
		return stderrors.Join(safeCause, err)
	}
	if !reconciled {
		return stderrors.Join(safeCause, errors.New(db.GatekeeperErrorStateConflict))
	}
	return safeCause
}

func safeGatekeeperError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(db.SafeGatekeeperErrorCode(err))
}

func (g *Gatekeeper) processChallengeActionWithStats(ctx context.Context, challenge *db.Challenge, recordStats bool) error {
	if challenge == nil {
		return nil
	}
	actionCtx, cancel := context.WithTimeout(ctx, challengeActionTimeout)
	defer cancel()
	owner := uuid.New()
	now := time.Now()
	leased, claimed, err := g.store.ClaimChallengeAction(
		actionCtx,
		challenge.ChallengeID,
		owner,
		now,
		now.Add(challengeActionLeaseDuration),
	)
	if err != nil || !claimed {
		return err
	}
	challenge = leased
	ctx = actionCtx
	entry := g.getLogEntry().WithFields(log.Fields{
		logFieldMethod:      "processChallengeAction",
		challengeIDLogField: challenge.ChallengeID,
		logFieldStatus:      challenge.Status,
	})
	moderationAvailable := true
	switch challenge.Status {
	case db.ChallengeStatusRestrictPending,
		db.ChallengeStatusApproveQueryPending,
		db.ChallengeStatusApproveMemberPending,
		db.ChallengeStatusUnrestrictPending,
		db.ChallengeStatusRejectPending:
		if g.banChecker == nil {
			moderationAvailable = false
		} else {
			available, err := g.banChecker.ModerationAvailable(ctx, challenge.ChatID)
			if err != nil {
				entry.WithField(logFieldErrorCode, db.SafeGatekeeperErrorCode(err)).Warn("failed to refresh moderation rights before challenge action")
			} else {
				moderationAvailable = available
			}
		}
		if !moderationAvailable && challenge.Status != db.ChallengeStatusRestrictPending {
			passed := challenge.Status != db.ChallengeStatusRejectPending
			finishErr := g.finishChallengeWithoutPrivileges(ctx, challenge, owner, passed, "moderation unavailable", recordStats)
			if finishErr == nil {
				return nil
			}
			return g.retryOrReconcileChallengeAction(ctx, challenge, owner, finishErr, entry)
		}
	}

	var actionErr error
	switch challenge.Status {
	case db.ChallengeStatusBanCheckPending:
		banned, checkErr := g.banChecker.CheckBan(ctx, challenge.UserID)
		if checkErr != nil {
			return g.retryOrReconcileChallengeAction(ctx, challenge, owner, checkErr, entry)
		}
		settings, settingsErr := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if settingsErr != nil {
			return g.retryOrReconcileChallengeAction(ctx, challenge, owner, settingsErr, entry)
		}
		if banned {
			if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseRejectBanStarted); err != nil {
				return err
			}
			if err := g.banChecker.BanUserWithMessage(ctx, challenge.ChatID, challenge.UserID, challenge.JoinMessageID); err != nil {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, err)
			}
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRejectBanDone); err != nil {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, err)
			}
		}
		if !banned && (!settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled) {
			deleted, deleteErr := g.store.DeleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, time.Now())
			if deleteErr != nil || !deleted {
				return stderrors.Join(deleteErr, errors.New("ban-check cleanup fence lost"))
			}
			return nil
		}
		nextStatus := db.ChallengeStatusPending
		if banned {
			nextStatus = db.ChallengeStatusRejectPending
		} else if challenge.WebAppToken == "" {
			nextStatus = db.ChallengeStatusWebAppFallbackPending
		}
		changed, transitionErr := g.store.CompleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, nextStatus, time.Time{}, time.Now())
		if transitionErr != nil || !changed {
			return stderrors.Join(transitionErr, errors.New("ban-check completion fence lost"))
		}
		challenge.Status = nextStatus
		challenge.ActionOwner = ""
		challenge.ActionLeaseUntil = sql.NullTime{}
		challenge.ActionPhase = db.ChallengePhaseReady
		if nextStatus == db.ChallengeStatusRejectPending {
			challenge.ActionPhase = db.ChallengePhaseRejectBanDone
		}
		challenge.ActionVersion++
		if challenge.WebAppToken == "" {
			challenge.JoinRequestQueryID = ""
		}
		if nextStatus == db.ChallengeStatusPending {
			return nil
		}
		return g.processChallengeActionWithStats(ctx, challenge, recordStats)
	case db.ChallengeStatusRestrictPending:
		restricted := challenge.UserRestricted
		if moderationAvailable && !restricted {
			if challenge.ActionPhase != db.ChallengePhaseRestrictDone {
				if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseRestrictStarted); err != nil {
					return err
				}
				_, actionErr = g.bot.RequestWithContext(ctx, api.RestrictChatMemberConfig{
					ChatMemberConfig: api.ChatMemberConfig{
						ChatConfig: api.ChatConfig{ChatID: challenge.ChatID},
						UserID:     challenge.UserID,
					},
					UntilDate: challenge.ExpiresAt.Unix(),
					Permissions: &api.ChatPermissions{
						CanSendMessages:       false,
						CanSendAudios:         false,
						CanSendDocuments:      false,
						CanSendPhotos:         false,
						CanSendVideos:         false,
						CanSendVideoNotes:     false,
						CanSendVoiceNotes:     false,
						CanSendPolls:          false,
						CanSendOtherMessages:  false,
						CanAddWebPagePreviews: false,
						CanChangeInfo:         false,
						CanInviteUsers:        false,
						CanPinMessages:        false,
						CanManageTopics:       false,
					},
				})
				if actionErr == nil || isTelegramRestrictionAlreadyApplied(actionErr) {
					restricted = true
					actionErr = nil
					version, persisted, persistErr := g.store.MarkLeasedChallengeRestrictedVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, db.ChallengePhaseRestrictStarted, time.Now())
					if persistErr != nil || !persisted {
						if persistErr == nil {
							persistErr = errors.New("restriction accepted after effect fence was lost")
						}
						return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, persistErr)
					}
					challenge.ActionVersion = version
					challenge.ActionPhase = db.ChallengePhaseRestrictDone
					challenge.UserRestricted = true
				} else if moderation.IsTelegramPrivilegeError(actionErr) {
					g.banChecker.MarkModerationUnavailable(challenge.ChatID)
					actionErr = nil
					if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRestrictDone); err != nil {
						return err
					}
				} else {
					return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, actionErr)
				}
			}
		}
		if actionErr == nil {
			settings, settingsErr := g.fetchAndValidateSettings(ctx, challenge.ChatID)
			if settingsErr != nil {
				actionErr = settingsErr
			} else {
				if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhasePublicMessageStarted); err != nil {
					return err
				}
				user := &api.User{ID: challenge.UserID, FirstName: "friend", LanguageCode: challenge.UserLanguage}
				target := &api.Chat{ID: challenge.ChatID}
				messageID, sendErr := g.sendChallengeMessage(ctx, challenge, user, target, challenge.ChatID, settings)
				if sendErr != nil {
					return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, sendErr)
				} else if messageID == 0 {
					actionErr = errors.New("public challenge text is empty")
				} else {
					bound, bindErr := g.store.BindLeasedChallengeMessage(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, db.ChallengePhasePublicMessageStarted, db.ChallengePhasePublicMessageDone, messageID, time.Now())
					if bindErr != nil || !bound {
						if bindErr == nil {
							bindErr = errors.New("public challenge message accepted after effect fence was lost")
						}
						return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, messageID, bindErr)
					}
					challenge.ActionVersion++
					challenge.ActionPhase = db.ChallengePhasePublicMessageDone
					completed, completeErr := g.store.CompleteLeasedChallengeActivationVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.ActionPhase, restricted, messageID, time.Now())
					if completeErr != nil || !completed {
						_ = bot.DeleteChatMessage(ctx, g.bot, challenge.CommChatID, messageID)
						return completeErr
					}
					if recordStats {
						g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengeStarted)
					}
					return nil
				}
			}
		}
	case db.ChallengeStatusWebAppFallbackPending:
		settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if err != nil {
			actionErr = err
		} else {
			actionErr = g.fallbackClaimedWebAppChallenge(ctx, challenge, owner, settings)
		}
		if actionErr == nil {
			return nil
		}
	case db.ChallengeStatusApproveQueryPending:
		if challenge.ActionPhase != db.ChallengePhaseQueryAnswerDone {
			if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseQueryAnswerStarted); err != nil {
				return err
			}
			actionErr = bot.AnswerJoinRequestQuery(ctx, g.bot, challenge.JoinRequestQueryID, bot.JoinRequestQueryResultApprove)
			if !isTelegramJoinQueryAlreadyApplied(actionErr) {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, actionErr)
			}
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseQueryAnswerDone); err != nil {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, err)
			}
		}
		if actionErr == nil {
			g.deleteChallengePrompt(ctx, challenge)
			changed, err := g.store.CompleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, db.ChallengePhaseQueryAnswerDone, db.ChallengeStatusPassedWaitingMemberJoin, time.Now().Add(approvedJoinRequestChallengeTTL), time.Now())
			if err != nil {
				return err
			}
			if changed && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
			}
			return nil
		}
	case db.ChallengeStatusApproveMemberPending:
		if challenge.ActionPhase != db.ChallengePhaseMemberApprovalDone {
			if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseMemberApprovalStarted); err != nil {
				return err
			}
			actionErr = bot.ApproveJoinRequest(ctx, g.bot, challenge.UserID, challenge.ChatID)
			if actionErr != nil && !isTelegramJoinApprovalAlreadyApplied(actionErr) {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, actionErr)
			}
			actionErr = nil
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseMemberApprovalDone); err != nil {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, err)
			}
		}
		if actionErr == nil {
			g.deleteChallengePrompt(ctx, challenge)
			changed, err := g.store.CompleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, db.ChallengePhaseMemberApprovalDone, db.ChallengeStatusPassedWaitingMemberJoin, time.Now().Add(approvedJoinRequestChallengeTTL), time.Now())
			if err != nil {
				return err
			}
			if changed && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
			}
			return nil
		}
	case db.ChallengeStatusUnrestrictPending:
		if !challenge.UserRestricted {
			return g.finishPassedChallengeWithoutEnforcement(ctx, challenge, recordStats)
		}
		if challenge.ActionPhase != db.ChallengePhaseUnrestrictDone {
			if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseUnrestrictStarted); err != nil {
				return err
			}
			actionErr = bot.UnrestrictChatting(ctx, g.bot, challenge.UserID, challenge.ChatID)
			if actionErr != nil && !isTelegramRemovalAlreadyApplied(actionErr) {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, actionErr)
			}
			actionErr = nil
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseUnrestrictDone); err != nil {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, err)
			}
		}
		if actionErr == nil {
			g.deleteChallengePrompt(ctx, challenge)
			deleted, err := g.store.DeleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, db.ChallengePhaseUnrestrictDone, time.Now())
			if err != nil {
				return err
			}
			if deleted && recordStats {
				g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
			}
			return nil
		}
	case db.ChallengeStatusRejectPending:
		if challenge.CommChatID == challenge.ChatID && (!challenge.UserRestricted || !moderationAvailable) {
			finishErr := g.finishChallengeWithoutPrivileges(ctx, challenge, owner, false, "moderation unavailable", recordStats)
			if finishErr == nil {
				return nil
			}
			return g.retryOrReconcileChallengeAction(ctx, challenge, owner, finishErr, entry)
		}
		if challenge.ActionPhase == db.ChallengePhaseReady && challenge.CommChatID != challenge.ChatID && moderationAvailable {
			currentMember, err := g.isCurrentJoinRequestMember(ctx, challenge)
			if err != nil {
				actionErr = err
				break
			}
			if currentMember {
				g.deleteChallengePrompt(ctx, challenge)
				if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRejectProbeDone); err != nil {
					return err
				}
				_, deleteErr := g.store.DeleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, time.Now())
				return deleteErr
			}
		}
		if challenge.ActionPhase == db.ChallengePhaseReady {
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRejectProbeDone); err != nil {
				return err
			}
		}
		if moderationAvailable && challenge.ActionPhase == db.ChallengePhaseRejectProbeDone {
			settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
			if err != nil {
				actionErr = err
				break
			}
			if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseRejectBanStarted); err != nil {
				return err
			}
			banErr := bot.BanUserFromChat(ctx, g.bot, challenge.UserID, challenge.ChatID, challenge.ExpiresAt.Add(settings.GetRejectTimeout()).Unix())
			if banErr != nil && !isTelegramBanAlreadyApplied(banErr) {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, banErr)
			}
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRejectBanDone); err != nil {
				return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, err)
			}
		} else if !moderationAvailable && challenge.ActionPhase == db.ChallengePhaseRejectProbeDone {
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRejectBanDone); err != nil {
				return err
			}
		}
		if challenge.ActionPhase == db.ChallengePhaseRejectBanDone {
			if challenge.JoinRequestQueryID != "" || challenge.CommChatID != challenge.ChatID {
				if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseRejectDeclineStarted); err != nil {
					return err
				}
				var declineErr error
				if challenge.JoinRequestQueryID != "" {
					declineErr = bot.AnswerJoinRequestQuery(ctx, g.bot, challenge.JoinRequestQueryID, bot.JoinRequestQueryResultDecline)
				} else {
					declineErr = bot.DeclineJoinRequest(ctx, g.bot, challenge.UserID, challenge.ChatID)
				}
				if declineErr != nil && !isTelegramJoinDeclineAlreadyApplied(declineErr) {
					return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, 0, declineErr)
				}
			}
			if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseRejectDeclineDone); err != nil {
				return err
			}
		}
		if challenge.ActionPhase == db.ChallengePhaseRejectDeclineDone {
			g.deleteChallengeMessages(ctx, challenge)
			deleted, err := g.store.DeleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, time.Now())
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
	if challenge.Status == db.ChallengeStatusWebAppFallbackPending && isTelegramConversationUnavailable(actionErr) {
		entry.WithField(logFieldErrorCode, db.SafeGatekeeperErrorCode(actionErr)).Info("DM fallback is permanently unavailable; declining join request")
		changed, err := g.store.CompleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, db.ChallengeStatusRejectPending, time.Time{}, time.Now())
		if err != nil || !changed {
			return err
		}
		challenge.Status = db.ChallengeStatusRejectPending
		return g.processChallengeAction(ctx, challenge)
	}
	if moderation.IsTelegramPrivilegeError(actionErr) {
		if g.banChecker != nil {
			g.banChecker.MarkModerationUnavailable(challenge.ChatID)
		}
		passed := challenge.Status == db.ChallengeStatusApproveQueryPending ||
			challenge.Status == db.ChallengeStatusApproveMemberPending ||
			challenge.Status == db.ChallengeStatusUnrestrictPending
		finishErr := g.finishChallengeWithoutPrivileges(ctx, challenge, owner, passed, db.SafeGatekeeperErrorCode(actionErr), recordStats)
		if finishErr == nil {
			return nil
		}
		return g.retryOrReconcileChallengeAction(ctx, challenge, owner, finishErr, entry)
	}
	return g.retryOrReconcileChallengeAction(ctx, challenge, owner, actionErr, entry)
}

func (g *Gatekeeper) retryOrReconcileChallengeAction(
	ctx context.Context,
	challenge *db.Challenge,
	owner string,
	actionErr error,
	entry *log.Entry,
) error {
	if challenge.AttemptCount+1 >= maxChallengeActionAttempts {
		reconciled, reconcileErr := g.store.ReconcileLeasedChallengeVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, 0, db.SafeGatekeeperErrorCode(actionErr), time.Now())
		if reconcileErr != nil {
			return stderrors.Join(safeGatekeeperError(actionErr), reconcileErr)
		}
		if reconciled {
			entry.WithFields(log.Fields{logFieldErrorCode: db.SafeGatekeeperErrorCode(actionErr), "attempt": challenge.AttemptCount + 1}).Error("gatekeeper action moved to operator reconciliation")
		}
		return safeGatekeeperError(actionErr)
	}
	nextAttemptAt := time.Now().Add(challengeRetryDelay(challenge.AttemptCount))
	scheduled, scheduleErr := g.store.ScheduleLeasedChallengeRetryVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, nextAttemptAt, db.SafeGatekeeperErrorCode(actionErr), time.Now())
	if scheduleErr != nil {
		return stderrors.Join(safeGatekeeperError(actionErr), scheduleErr)
	}
	if scheduled {
		fields := log.Fields{logFieldErrorCode: db.SafeGatekeeperErrorCode(actionErr), "attempt": challenge.AttemptCount + 1}
		entry.WithFields(fields).WithField("retry_in", time.Until(nextAttemptAt)).Warn("gatekeeper action failed; retry scheduled")
	}
	return safeGatekeeperError(actionErr)
}

func (g *Gatekeeper) finishPassedChallengeWithoutEnforcement(ctx context.Context, challenge *db.Challenge, recordStats bool) error {
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

func (g *Gatekeeper) finishLeasedPassedChallengeWithoutEnforcement(ctx context.Context, challenge *db.Challenge, owner string, recordStats bool) error {
	g.deleteChallengePrompt(ctx, challenge)
	deleted, err := g.store.DeleteLeasedChallengeActionVersion(ctx, challenge.ChallengeID, owner, challenge.ActionVersion, challenge.Status, challenge.ActionPhase, time.Now())
	if err != nil {
		return err
	}
	if deleted && recordStats {
		g.incrementChallengeStat(ctx, challenge.ChatID, handlersbase.StatChallengePassed)
	}
	return nil
}

func (g *Gatekeeper) finishChallengeWithoutPrivileges(ctx context.Context, challenge *db.Challenge, owner string, passed bool, lastError string, recordStats bool) error {
	if challenge == nil {
		return nil
	}
	if passed && challenge.CommChatID == challenge.ChatID {
		return g.finishLeasedPassedChallengeWithoutEnforcement(ctx, challenge, owner, recordStats)
	}

	language := g.s.GetLanguage(ctx, challenge.ChatID, nil)
	mention := fmt.Sprintf("[%s](tg://user?id=%d)", api.EscapeText(api.ModeMarkdown, i18n.Get("This user", language)), challenge.UserID)
	messageTemplate := i18n.Get("⚠️ %s did not pass the CAPTCHA. I cannot remove this user because I do not have permission to restrict members.", language)
	if passed {
		messageTemplate = i18n.Get("⚠️ %s passed the CAPTCHA, but I cannot approve the join request because I do not have the required administrator rights.", language)
	}
	notice := api.NewMessage(challenge.ChatID, fmt.Sprintf(messageTemplate, mention))
	notice.ParseMode = api.ModeMarkdown
	notice.DisableNotification = false
	if err := g.beginChallengeEffect(ctx, challenge, owner, db.ChallengePhaseNoticeMessageStarted); err != nil {
		return err
	}
	sent, err := bot.Send(ctx, g.bot, notice)
	if err != nil {
		g.getLogEntry().WithFields(log.Fields{
			challengeIDLogField: challenge.ChallengeID,
			logFieldErrorCode:   db.SafeGatekeeperErrorCode(err),
		}).Error("failed to send no-rights challenge notice")
		expiresAt := time.Now().Add(noPrivilegesNoticeRetention)
		archiveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		archived, archiveErr := g.store.ArchiveLeasedNoticeFailureVersion(
			archiveCtx,
			challenge.ChallengeID,
			owner,
			challenge.ActionVersion,
			challenge.Status,
			challenge.ActionPhase,
			expiresAt,
			db.SafeGatekeeperErrorCode(err),
			time.Now(),
		)
		cancel()
		if archiveErr != nil || !archived {
			return stderrors.Join(safeGatekeeperError(err), archiveErr)
		}
		return safeGatekeeperError(err)
	}
	if err := g.advanceChallengePhase(ctx, challenge, owner, db.ChallengePhaseNoticeMessageDone); err != nil {
		return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, sent.MessageID, err)
	}

	expiresAt := time.Now().Add(noPrivilegesNoticeRetention)
	completed, err := g.store.CompleteLeasedChallengeWithoutPrivilegesVersion(
		ctx,
		challenge.ChallengeID,
		owner,
		challenge.ActionVersion,
		challenge.Status,
		challenge.ActionPhase,
		sent.MessageID,
		expiresAt,
		lastError,
		time.Now(),
	)
	if err != nil || !completed {
		if err == nil {
			err = errors.New("notice accepted after effect fence was lost")
		}
		return g.reconcileAmbiguousChallengeEffect(ctx, challenge, owner, sent.MessageID, err)
	}
	challenge.NoticeMessageID = sent.MessageID
	challenge.ExpiresAt = expiresAt
	challenge.Status = db.ChallengeStatusNoPrivilegesNotice
	g.deleteChallengePrompt(ctx, challenge)
	if recordStats {
		stat := handlersbase.StatChallengeFailed
		if passed {
			stat = handlersbase.StatChallengePassed
		}
		g.incrementChallengeStat(ctx, challenge.ChatID, stat)
	}
	return nil
}

func (g *Gatekeeper) cleanupNoPrivilegesNotice(ctx context.Context, challenge *db.Challenge) error {
	if challenge == nil {
		return nil
	}
	g.deleteChallengePrompt(ctx, challenge)
	if challenge.NoticeMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, g.bot, challenge.ChatID, challenge.NoticeMessageID); err != nil && !isTelegramMessageAlreadyDeleted(err) {
			return err
		}
	}
	_, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, db.ChallengeStatusNoPrivilegesNotice)
	return err
}

func (g *Gatekeeper) deleteChallengeMessages(ctx context.Context, challenge *db.Challenge) {
	g.deleteChallengePrompt(ctx, challenge)
	entry := g.getLogEntry().WithField(challengeIDLogField, challenge.ChallengeID)
	if challenge.JoinMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, g.bot, challenge.ChatID, challenge.JoinMessageID); err != nil && !isTelegramMessageAlreadyDeleted(err) {
			entry.WithField(logFieldErrorCode, db.SafeGatekeeperErrorCode(err)).Warn("failed to delete join message")
		}
	}
}

func (g *Gatekeeper) deleteChallengePrompt(ctx context.Context, challenge *db.Challenge) {
	entry := g.getLogEntry().WithField(challengeIDLogField, challenge.ChallengeID)
	if challenge.ChallengeMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, g.bot, challenge.CommChatID, challenge.ChallengeMessageID); err != nil && !isTelegramMessageAlreadyDeleted(err) {
			entry.WithField(logFieldErrorCode, db.SafeGatekeeperErrorCode(err)).Warn("failed to delete challenge message")
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

func isTelegramMessageAlreadyDeleted(err error) bool {
	return telegramErrorContains(err, "MESSAGE TO DELETE NOT FOUND", "MESSAGE_ID_INVALID")
}

func isTelegramRestrictionAlreadyApplied(err error) bool {
	return err == nil
}

func isTelegramJoinQueryAlreadyApplied(err error) bool {
	return err == nil
}

func isTelegramJoinApprovalAlreadyApplied(err error) bool {
	return telegramErrorContains(err, "USER_ALREADY_PARTICIPANT")
}

func isTelegramRemovalAlreadyApplied(err error) bool {
	return telegramErrorContains(
		err,
		"USER_NOT_PARTICIPANT",
		"USER NOT PARTICIPANT",
		"PARTICIPANT_ID_INVALID",
		"MEMBER NOT FOUND",
		"USER IS DEACTIVATED",
	)
}

func isTelegramBanAlreadyApplied(err error) bool {
	return telegramErrorContains(err, "USER IS DEACTIVATED")
}

func isTelegramJoinDeclineAlreadyApplied(err error) bool {
	return telegramErrorContains(
		err,
		"HIDE_REQUESTER_MISSING",
		"USER_NOT_PARTICIPANT",
		"USER NOT PARTICIPANT",
		"PARTICIPANT_ID_INVALID",
		"MEMBER NOT FOUND",
		"USER IS DEACTIVATED",
	)
}

func telegramErrorContains(err error, markers ...string) bool {
	if err == nil {
		return true
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isTelegramConversationUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range []string{
		"BOT CAN'T INITIATE CONVERSATION",
		"BOT_CANT_INITIATE_CONVERSATION",
		"BOT WAS BLOCKED BY THE USER",
		"USER IS DEACTIVATED",
		"CHAT NOT FOUND",
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
		if isTelegramRemovalAlreadyApplied(err) {
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
