package db

import (
	"testing"
	"time"
)

func TestSettingsLegacySecondTimeoutsAreReadWithoutMutation(t *testing.T) {
	t.Parallel()

	settings := &Settings{
		ChallengeTimeout: 180,
		RejectTimeout:    600,
	}

	if got := settings.GetChallengeTimeout(); got != 3*time.Minute {
		t.Fatalf("unexpected challenge timeout: got %s want %s", got, 3*time.Minute)
	}
	if got := settings.GetRejectTimeout(); got != 10*time.Minute {
		t.Fatalf("unexpected reject timeout: got %s want %s", got, 10*time.Minute)
	}
	if settings.ChallengeTimeout != 180 {
		t.Fatalf("challenge timeout getter mutated settings: %d", settings.ChallengeTimeout)
	}
	if settings.RejectTimeout != 600 {
		t.Fatalf("reject timeout getter mutated settings: %d", settings.RejectTimeout)
	}
}

func TestDefaultSettingsEnableCommunityVoting(t *testing.T) {
	t.Parallel()

	settings := DefaultSettings(42)
	if !settings.CommunityVotingEnabled {
		t.Fatal("expected community voting to be enabled by default")
	}
}

func TestDefaultSettingsEnableGatekeeperCaptcha(t *testing.T) {
	t.Parallel()

	settings := DefaultSettings(42)
	if !settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled {
		t.Fatalf("expected gatekeeper captcha to be enabled by default: %#v", settings)
	}
}
