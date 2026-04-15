package handlers

import (
	"context"
	"strconv"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

const panelRecommendedProtectionHandledKeyPrefix = "admin_panel:recommended_protection_handled:"

func applyRecommendedProtectionSettings(settings *db.Settings) {
	if settings == nil {
		return
	}

	settings.GatekeeperEnabled = true
	settings.GatekeeperCaptchaEnabled = true
	settings.GatekeeperGreetingEnabled = false
	settings.ChallengeTimeout = (3 * time.Minute).Nanoseconds()
	settings.RejectTimeout = (10 * time.Minute).Nanoseconds()
	settings.LLMFirstMessageEnabled = true
	settings.CommunityVotingEnabled = false
}

func hasRecommendedProtection(state *panelState) bool {
	if state == nil {
		return false
	}

	return state.Features.GatekeeperEnabled &&
		state.Features.GatekeeperCaptchaEnabled &&
		!state.Features.GatekeeperGreetingEnabled &&
		time.Duration(state.ChallengeTimeout) == 3*time.Minute &&
		time.Duration(state.RejectTimeout) == 10*time.Minute &&
		state.Features.LLMFirstMessageEnabled &&
		!state.Features.CommunityVotingEnabled
}

func hasCustomizedSettings(state *panelState) bool {
	if state == nil {
		return false
	}

	defaultSettings := db.DefaultSettings(state.ChatID)

	return state.Language != defaultSettings.Language ||
		state.Features.GatekeeperEnabled != defaultSettings.GatekeeperEnabled ||
		state.Features.GatekeeperCaptchaEnabled != defaultSettings.GatekeeperCaptchaEnabled ||
		state.Features.GatekeeperGreetingEnabled != defaultSettings.GatekeeperGreetingEnabled ||
		state.GatekeeperCaptchaOptionsCount != defaultSettings.GatekeeperCaptchaOptionsCount ||
		state.GatekeeperGreetingText != defaultSettings.GatekeeperGreetingText ||
		state.Features.LLMFirstMessageEnabled != defaultSettings.LLMFirstMessageEnabled ||
		state.Features.CommunityVotingEnabled != defaultSettings.CommunityVotingEnabled ||
		state.CommunityVotingTimeoutOverrideNS != defaultSettings.CommunityVotingTimeoutOverrideNS ||
		state.CommunityVotingMinVotersOverride != defaultSettings.CommunityVotingMinVotersOverride ||
		state.CommunityVotingMaxVotersOverride != defaultSettings.CommunityVotingMaxVotersOverride ||
		state.CommunityVotingMinVotersPercentOverride != defaultSettings.CommunityVotingMinVotersPercentOverride ||
		state.ChallengeTimeout != defaultSettings.ChallengeTimeout ||
		state.RejectTimeout != defaultSettings.RejectTimeout
}

func (a *Admin) shouldShowRecommendedProtection(ctx context.Context, state *panelState) (bool, error) {
	if state == nil {
		return false, nil
	}

	handled, err := a.s.GetDB().GetKV(ctx, recommendedProtectionHandledKey(state.ChatID))
	if err != nil {
		return false, err
	}
	if handled != "" {
		return false, nil
	}

	if hasCustomizedSettings(state) {
		if err := a.markRecommendedProtectionHandled(ctx, state.ChatID); err != nil {
			return false, err
		}
		return false, nil
	}

	if err := a.markRecommendedProtectionHandled(ctx, state.ChatID); err != nil {
		return false, err
	}

	return true, nil
}

func (a *Admin) markRecommendedProtectionHandled(ctx context.Context, chatID int64) error {
	return a.s.GetDB().SetKV(ctx, recommendedProtectionHandledKey(chatID), "1")
}

func recommendedProtectionHandledKey(chatID int64) string {
	return panelRecommendedProtectionHandledKeyPrefix + strconv.FormatInt(chatID, 10)
}
