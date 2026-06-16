package config

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

const redactedPlaceholder = "[REDACTED]"

var (
	// telegramTokenInURL matches the bot token embedded in Telegram API URLs,
	// e.g. https://api.telegram.org/bot<id>:<secret>/getUpdates. The non-secret
	// numeric id prefix is preserved to keep diagnostics useful.
	telegramTokenInURL = regexp.MustCompile(`bot([0-9]{6,}):[A-Za-z0-9_-]{30,}`)
	// telegramTokenBare matches a standalone bot token (digits:secret) in its
	// canonical shape. The leading boundary avoids matching inside a longer run
	// of digits; the secret part is at least 30 chars to skip ordinary "id:value"
	// strings while still covering real 35-char tokens.
	telegramTokenBare = regexp.MustCompile(`(^|[^0-9A-Za-z_-])([0-9]{6,}:[A-Za-z0-9_-]{30,})`)

	secretsMu      sync.RWMutex
	secretLiterals []string
)

// RegisterSecret records a literal secret value to be masked from all log
// output. Registering at startup (e.g. the configured bot token and API keys)
// ensures the exact value is scrubbed even if it appears outside a recognized
// pattern. Short or empty values are ignored to avoid masking common text.
func RegisterSecret(secret string) {
	if len(secret) < 8 {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, existing := range secretLiterals {
		if existing == secret {
			return
		}
	}
	secretLiterals = append(secretLiterals, secret)
	// Mask longer secrets first so a registered value is not partially consumed
	// by a shorter overlapping one.
	sort.Slice(secretLiterals, func(i, j int) bool {
		return len(secretLiterals[i]) > len(secretLiterals[j])
	})
}

// Redact masks Telegram bot tokens and any registered literal secrets from s.
// It takes a fast path when no secret marker is present and never panics.
func Redact(s string) string {
	if s == "" {
		return s
	}

	secretsMu.RLock()
	literals := secretLiterals
	secretsMu.RUnlock()

	for _, literal := range literals {
		if strings.Contains(s, literal) {
			s = strings.ReplaceAll(s, literal, redactedPlaceholder)
		}
	}

	// The colon is the cheap discriminator shared by both token shapes; skip the
	// regex machinery entirely when it is absent.
	if !strings.Contains(s, ":") {
		return s
	}
	s = telegramTokenInURL.ReplaceAllString(s, "bot$1:"+redactedPlaceholder)
	s = telegramTokenBare.ReplaceAllString(s, "$1"+redactedPlaceholder)
	return s
}
