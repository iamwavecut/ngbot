package handlers

import "time"

const (
	panelExamplesPageSize = 5
	panelLanguagePageSize = 6
	panelPreviewMaxLen    = 80
	panelMaxInputLen      = 4096
	panelLLMExamplesCap   = 20
)

var (
	panelGatekeeperCaptchaOptions = []int{3, 4, 5, 6, 8, 10}
	panelChallengeTimeoutOptions  = []time.Duration{
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
		3 * time.Minute,
		5 * time.Minute,
	}
	panelRejectTimeoutOptions = []time.Duration{
		10 * time.Minute,
	}
	panelVotingTimeoutOptions = []time.Duration{
		1 * time.Minute,
		2 * time.Minute,
		3 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
	}
	panelVotingMinVotersOptions        = []int{1, 2, 3, 5, 10}
	panelVotingMaxVotersOptions        = []int{0, 5, 10, 20}
	panelVotingMinVotersPercentOptions = []int{1, 3, 5, 10, 20}
)

const (
	panelSessionTTL      = time.Hour
	panelCleanupInterval = 5 * time.Minute
	panelTypingInterval  = 7 * time.Second
)
