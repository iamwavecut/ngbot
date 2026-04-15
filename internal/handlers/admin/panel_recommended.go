package handlers

import (
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

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
