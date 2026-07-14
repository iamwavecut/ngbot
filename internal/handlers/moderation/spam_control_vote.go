package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	log "github.com/sirupsen/logrus"
)

func (sc *SpamControl) RecordVote(ctx context.Context, caseID int64, voterID int64, vote bool) (int, int, error) {
	spamCase, err := sc.store.GetSpamCase(ctx, caseID)
	if err != nil {
		return 0, 0, err
	}
	if spamCase.UserID == voterID {
		return 0, 0, ErrSuspectCannotVote
	}
	settings, err := sc.s.GetSettings(ctx, spamCase.ChatID)
	if err != nil {
		return 0, 0, err
	}
	if settings == nil {
		settings = db.DefaultSettings(spamCase.ChatID)
	}
	if !settings.CommunityVotingEnabled {
		return 0, 0, ErrCommunityVotingDisabled
	}
	eligible, err := sc.isEligibleVoter(ctx, spamCase.ChatID, voterID)
	if err != nil {
		return 0, 0, err
	}
	if !eligible {
		return 0, 0, ErrVoterNotEligible
	}

	notSpamVotes, spamVotes, accepted, err := sc.store.AddVoteIfPending(ctx, &db.SpamVote{
		CaseID:  caseID,
		VoterID: voterID,
		Vote:    vote,
		VotedAt: time.Now(),
	})
	if err != nil {
		return 0, 0, err
	}
	if !accepted {
		return notSpamVotes, spamVotes, ErrSpamCaseClosed
	}

	required, err := sc.requiredVoters(ctx, caseID)
	if err != nil {
		return notSpamVotes, spamVotes, err
	}
	if notSpamVotes+spamVotes >= required {
		if err := sc.ResolveCase(ctx, caseID, false); err != nil {
			return notSpamVotes, spamVotes, err
		}
	}

	return notSpamVotes, spamVotes, nil
}

func (sc *SpamControl) isEligibleVoter(ctx context.Context, chatID, voterID int64) (bool, error) {
	member, err := sc.s.IsMember(ctx, chatID, voterID)
	if err != nil {
		return false, fmt.Errorf("check stored membership: %w", err)
	}
	if member {
		return true, nil
	}
	chatMember, err := bot.GetChatMember(ctx, sc.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     voterID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("verify voter membership: %w", err)
	}
	return !chatMember.HasLeft() && !chatMember.WasKicked(), nil
}

func (sc *SpamControl) requiredVoters(ctx context.Context, caseID int64) (int, error) {
	spamCase, err := sc.store.GetSpamCase(ctx, caseID)
	if err != nil {
		return 0, err
	}
	policy := sc.effectiveVotingPolicy(ctx, spamCase.ChatID)

	members, err := sc.store.GetMembers(ctx, spamCase.ChatID)
	if err != nil {
		return 0, fmt.Errorf("get members count: %w", err)
	}

	minVotersFromPercentage := int(float64(len(members)) * policy.MinVotersPercentage / 100)
	required := max(policy.MinVoters, minVotersFromPercentage)
	if policy.MaxVoters > 0 && required > policy.MaxVoters {
		required = policy.MaxVoters
	}
	if required < 1 {
		required = 1
	}

	return required, nil
}

func (sc *SpamControl) ResolveCase(ctx context.Context, caseID int64, timedOut bool) error {
	entry := sc.getLogEntry().WithField("method", "resolveCase").WithField("case_id", caseID)
	requiredVoters, err := sc.requiredVoters(ctx, caseID)
	if err != nil {
		return fmt.Errorf("failed to calculate required voters: %w", err)
	}

	spamCase, claimed, err := sc.store.ClaimSpamCaseResolution(ctx, caseID, requiredVoters, timedOut, time.Now())
	if err != nil {
		return fmt.Errorf("claim case resolution: %w", err)
	}
	if !claimed {
		entry.WithField("required_voters", requiredVoters).Debug("resolution deferred or already claimed")
		return nil
	}
	return sc.resolveClaimedCase(ctx, spamCase)
}

func (sc *SpamControl) resolveClaimedCase(ctx context.Context, spamCase *db.SpamCase) error {
	if spamCase == nil {
		return nil
	}
	var actionErr error
	terminalStatus := db.SpamCaseStatusFalsePositive
	statMetric := handlersbase.StatFalsePositive
	switch spamCase.Status {
	case db.SpamCaseStatusResolvingSpam:
		terminalStatus = db.SpamCaseStatusSpam
		statMetric = handlersbase.StatSpamConfirmed
		if !spamCase.PreVoteRestricted && spamCase.MessageID != 0 {
			if err := bot.DeleteChatMessage(ctx, sc.bot, spamCase.ChatID, spamCase.MessageID); err != nil {
				log.WithField("error", err.Error()).WithField("chat_id", spamCase.ChatID).WithField("message_id", spamCase.MessageID).Error("failed to delete reported spam message")
			}
		}
		if err := bot.BanUserFromChat(ctx, sc.bot, spamCase.UserID, spamCase.ChatID, 0); err != nil {
			log.WithField("error", err.Error()).Error("failed to ban user")
			actionErr = err
		} else {
			sc.cleanupRecentJoinMessage(ctx, spamCase.ChatID, spamCase.UserID)
		}
		sc.clearKnownNonMember(ctx, spamCase.ChatID, spamCase.UserID)
	case db.SpamCaseStatusResolvingFalsePositive:
		if spamCase.PreVoteRestricted {
			if err := sc.banService.UnmuteUser(ctx, spamCase.ChatID, spamCase.UserID); err != nil {
				log.WithField("error", err.Error()).Error("failed to unmute user")
				actionErr = err
			}
		}
	default:
		return nil
	}
	if actionErr != nil {
		nextAttemptAt := time.Time{}
		if spamCase.AttemptCount+1 < maxSpamResolutionAttempts {
			nextAttemptAt = time.Now().Add(spamCaseRetryDelay(spamCase.AttemptCount))
		}
		scheduled, scheduleErr := sc.store.ScheduleSpamCaseRetry(ctx, spamCase.ID, spamCase.Status, nextAttemptAt, actionErr.Error())
		if scheduled && nextAttemptAt.IsZero() {
			sc.getLogEntry().WithField("case_id", spamCase.ID).WithField("attempt", spamCase.AttemptCount+1).WithField("error", actionErr.Error()).Error("spam resolution retries exhausted; durable state retained")
		}
		return errors.Join(actionErr, scheduleErr)
	}
	now := time.Now()
	statsKey := handlersbase.StatsKey(spamCase.ChatID, now, statMetric)
	finalized, err := sc.store.FinalizeSpamCaseResolution(ctx, spamCase.ID, spamCase.Status, terminalStatus, statsKey, now)
	if err != nil {
		return fmt.Errorf("finalize case: %w", err)
	}
	if finalized {
		spamCase.Status = terminalStatus
		spamCase.ResolvedAt = &now
		sc.closeVotingPrompt(ctx, spamCase)
	}
	return nil
}

func (sc *SpamControl) closeVotingPrompt(ctx context.Context, spamCase *db.SpamCase) {
	if spamCase == nil {
		return
	}
	if spamCase.NotificationMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, sc.bot, spamCase.ChatID, spamCase.NotificationMessageID); err != nil && !isSpamTelegramEffectAlreadyApplied(err) {
			log.WithField("error", err.Error()).WithField("case_id", spamCase.ID).Debug("failed to delete in-chat voting prompt")
		}
	}
	if spamCase.ChannelPostID != 0 && spamCase.ChannelUsername != "" {
		emptyMarkup := api.NewInlineKeyboardMarkup()
		edit := api.EditMessageReplyMarkupConfig{BaseEdit: api.BaseEdit{
			BaseChatMessage: api.BaseChatMessage{
				ChatConfig: api.ChatConfig{ChannelUsername: "@" + strings.TrimPrefix(spamCase.ChannelUsername, "@")},
				MessageID:  spamCase.ChannelPostID,
			},
			ReplyMarkup: &emptyMarkup,
		}}
		if _, err := sc.bot.RequestWithContext(ctx, edit); err != nil && !isSpamTelegramEffectAlreadyApplied(err) {
			log.WithField("error", err.Error()).WithField("case_id", spamCase.ID).Debug("failed to close log-channel voting prompt")
		}
	}
}

func resolveStatusFromVotes(votes []*db.SpamVote, requiredVoters int, timedOut bool) (string, bool) {
	spamVotes := 0
	notSpamVotes := 0
	for _, vote := range votes {
		if vote.Vote {
			notSpamVotes++
		} else {
			spamVotes++
		}
	}

	if len(votes) < requiredVoters {
		if timedOut {
			return spamCaseStatusFalsePositive, true
		}
		return "", false
	}
	if spamVotes == notSpamVotes {
		if timedOut {
			return spamCaseStatusFalsePositive, true
		}
		return "", false
	}
	if spamVotes > notSpamVotes {
		return spamCaseStatusSpam, true
	}
	return spamCaseStatusFalsePositive, true
}

func (sc *SpamControl) effectiveVotingPolicy(ctx context.Context, chatID int64) votingPolicy {
	policy := resolveVotingPolicy(sc.config, nil)
	settings, err := sc.s.GetSettings(ctx, chatID)
	if err != nil || settings == nil {
		return policy
	}
	return resolveVotingPolicy(sc.config, settings)
}

func normalizeVotingPolicy(policy votingPolicy) votingPolicy {
	if policy.Timeout <= 0 {
		policy.Timeout = 5 * time.Minute
	}
	if policy.MinVoters < 1 {
		policy.MinVoters = 1
	}
	if policy.MaxVoters < 0 {
		policy.MaxVoters = 0
	}
	if policy.MinVotersPercentage < 0 {
		policy.MinVotersPercentage = 0
	}
	return policy
}

func resolveVotingPolicy(base config.SpamControl, settings *db.Settings) votingPolicy {
	policy := votingPolicy{
		Timeout:             base.VotingTimeoutMinutes,
		MinVoters:           base.MinVoters,
		MaxVoters:           base.MaxVoters,
		MinVotersPercentage: base.MinVotersPercentage,
	}

	if settings == nil {
		return normalizeVotingPolicy(policy)
	}
	if settings.CommunityVotingTimeoutOverrideNS != int64(db.SettingsOverrideInherit) {
		policy.Timeout = time.Duration(settings.CommunityVotingTimeoutOverrideNS)
	}
	if settings.CommunityVotingMinVotersOverride != db.SettingsOverrideInherit {
		policy.MinVoters = settings.CommunityVotingMinVotersOverride
	}
	if settings.CommunityVotingMaxVotersOverride != db.SettingsOverrideInherit {
		policy.MaxVoters = settings.CommunityVotingMaxVotersOverride
	}
	if settings.CommunityVotingMinVotersPercentOverride != db.SettingsOverrideInherit {
		policy.MinVotersPercentage = float64(settings.CommunityVotingMinVotersPercentOverride)
	}

	return normalizeVotingPolicy(policy)
}
