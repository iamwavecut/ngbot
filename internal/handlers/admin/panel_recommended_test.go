package handlers

import (
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestApplyRecommendedProtectionSettings(t *testing.T) {
	t.Parallel()

	settings := db.DefaultSettings(42)
	settings.GatekeeperEnabled = false
	settings.GatekeeperCaptchaEnabled = false
	settings.GatekeeperGreetingEnabled = true
	settings.LLMFirstMessageEnabled = false
	settings.CommunityVotingEnabled = true

	applyRecommendedProtectionSettings(settings)

	if !settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled {
		t.Fatalf("expected gatekeeper captcha baseline to be enabled: %#v", settings)
	}
	if settings.GatekeeperGreetingEnabled {
		t.Fatalf("expected greeting to be disabled in recommended baseline")
	}
	if settings.GetChallengeTimeout() != 3*time.Minute {
		t.Fatalf("unexpected challenge timeout: %s", settings.GetChallengeTimeout())
	}
	if settings.GetRejectTimeout() != 10*time.Minute {
		t.Fatalf("unexpected reject timeout: %s", settings.GetRejectTimeout())
	}
	if !settings.LLMFirstMessageEnabled {
		t.Fatalf("expected LLM first message to be enabled")
	}
	if settings.CommunityVotingEnabled {
		t.Fatalf("expected community voting to be disabled")
	}
}

func TestHasRecommendedProtection(t *testing.T) {
	t.Parallel()

	state := &panelState{
		Features: panelFeatureFlags{
			GatekeeperEnabled:         true,
			GatekeeperCaptchaEnabled:  true,
			GatekeeperGreetingEnabled: false,
			LLMFirstMessageEnabled:    true,
			CommunityVotingEnabled:    false,
		},
		ChallengeTimeout: (3 * time.Minute).Nanoseconds(),
		RejectTimeout:    (10 * time.Minute).Nanoseconds(),
	}

	if !hasRecommendedProtection(state) {
		t.Fatalf("expected recommended protection to be detected")
	}

	state.Features.CommunityVotingEnabled = true
	if hasRecommendedProtection(state) {
		t.Fatalf("expected recommended protection to be false after enabling voting")
	}
}
