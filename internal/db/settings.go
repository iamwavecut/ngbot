package db

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

func DefaultSettings(chatID int64) *Settings {
	return &Settings{
		ID:                            chatID,
		Enabled:                       true,
		GatekeeperEnabled:             false,
		GatekeeperCaptchaEnabled:      false,
		GatekeeperGreetingEnabled:     false,
		GatekeeperCaptchaOptionsCount: 5,
		GatekeeperGreetingText:        "",
		LLMFirstMessageEnabled:        true,
		CommunityVotingEnabled:        true,
		ChallengeTimeout:              (3 * time.Minute).Nanoseconds(),
		RejectTimeout:                 (10 * time.Minute).Nanoseconds(),
		Language:                      "en",
	}
}
