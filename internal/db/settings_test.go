package db

import (
	"testing"
	"time"
)

func TestSettingsLegacySecondTimeoutsAreNormalized(t *testing.T) {
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
	if settings.ChallengeTimeout != (3 * time.Minute).Nanoseconds() {
		t.Fatalf("expected normalized challenge timeout to be persisted in settings, got %d", settings.ChallengeTimeout)
	}
	if settings.RejectTimeout != (10 * time.Minute).Nanoseconds() {
		t.Fatalf("expected normalized reject timeout to be persisted in settings, got %d", settings.RejectTimeout)
	}
}
