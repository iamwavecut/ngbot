package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
)

const maxSpamResolutionAttempts = 8

func (sc *SpamControl) runDurableWorker(ctx context.Context) {
	if err := sc.recoverPendingSpamCaseDeadlines(ctx); err != nil && !errors.Is(err, context.Canceled) {
		sc.getLogEntry().WithField("error", err.Error()).Error("failed to recover spam case deadlines")
	}
	sc.processDurableWork(ctx)
	ticker := time.NewTicker(spamWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sc.processDurableWork(ctx)
		}
	}
}

func (sc *SpamControl) recoverPendingSpamCaseDeadlines(ctx context.Context) error {
	cases, err := sc.store.GetPendingSpamCases(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, spamCase := range cases {
		if spamCase == nil || spamCase.ResolveAt != nil {
			continue
		}
		if !spamCase.PreVoteRestricted && spamCase.MessageID != 0 {
			claimedCase, claimed, claimErr := sc.store.ClaimKnownSpamCase(ctx, spamCase.ID, time.Now())
			if claimErr != nil {
				result = errors.Join(result, claimErr)
				continue
			}
			if claimed {
				result = errors.Join(result, sc.resolveClaimedCase(ctx, claimedCase))
			}
			continue
		}
		resolveAt := spamCase.CreatedAt.Add(sc.effectiveVotingPolicy(ctx, spamCase.ChatID).Timeout)
		if _, err := sc.store.SetSpamCaseResolveAt(ctx, spamCase.ID, resolveAt); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (sc *SpamControl) processDurableWork(ctx context.Context) {
	cases, err := sc.store.GetDueSpamCases(ctx, time.Now())
	if err != nil {
		sc.getLogEntry().WithField("error", err.Error()).Error("failed to load due spam cases")
	} else {
		for _, spamCase := range cases {
			var resolveErr error
			if spamCase.Status == db.SpamCaseStatusPending {
				resolveErr = sc.ResolveCase(ctx, spamCase.ID, true)
			} else {
				resolveErr = sc.resolveClaimedCase(ctx, spamCase)
			}
			if resolveErr != nil && !errors.Is(resolveErr, context.Canceled) {
				sc.getLogEntry().WithField("case_id", spamCase.ID).WithField("error", resolveErr.Error()).Error("failed to process durable spam resolution")
			}
		}
	}
	sc.cleanupDueReportMessages(ctx, time.Now().Add(-voteBanReportMessageRetention))
}

func (sc *SpamControl) cleanupDueReportMessages(ctx context.Context, before time.Time) {
	messages, err := sc.store.GetDueSpamCaseReportMessages(ctx, before)
	if err != nil {
		sc.getLogEntry().WithField("error", err.Error()).Error("failed to load due report messages")
		return
	}
	for _, message := range messages {
		err := bot.DeleteChatMessage(ctx, sc.bot, message.ChatID, message.MessageID)
		if err != nil && !isSpamTelegramEffectAlreadyApplied(err) {
			sc.getLogEntry().WithField("error", err.Error()).WithField("case_id", message.CaseID).Error("failed to delete report message")
			continue
		}
		if err := sc.store.DeleteSpamCaseReportMessage(ctx, message.CaseID, message.ChatID, message.MessageID); err != nil {
			sc.getLogEntry().WithField("error", err.Error()).WithField("case_id", message.CaseID).Error("failed to acknowledge report message deletion")
		}
	}
}

func spamCaseRetryDelay(attempt int) time.Duration {
	return min(5*time.Second*time.Duration(1<<min(attempt, 8)), 15*time.Minute)
}

func isSpamTelegramEffectAlreadyApplied(err error) bool {
	if err == nil {
		return true
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range []string{
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
