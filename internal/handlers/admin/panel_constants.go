package handlers

import "time"

const (
	panelExamplesPageSize = 5
	panelLanguagePageSize = 6
	panelPreviewMaxLen    = 80
	panelMaxInputLen      = 4096
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
)

const (
	panelSessionTTL      = time.Hour
	panelCleanupInterval = 5 * time.Minute
	panelTypingInterval  = 7 * time.Second
)
