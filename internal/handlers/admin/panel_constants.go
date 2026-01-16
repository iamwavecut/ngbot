package handlers

import "time"

const (
	panelExamplesPageSize = 5
	panelLanguagePageSize = 5
	panelPreviewMaxLen    = 80
	panelMaxInputLen      = 4096
)

const (
	panelSessionTTL      = time.Hour
	panelCleanupInterval = 5 * time.Minute
	panelTypingInterval  = 7 * time.Second
)
