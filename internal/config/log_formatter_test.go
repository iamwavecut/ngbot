package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestFormatterPreservesErrorsAndRedactsSecrets(t *testing.T) {
	t.Parallel()

	const secret = "formatter-secret-value-123456"
	RegisterSecret(secret)
	entry := &log.Entry{
		Logger:  log.New(),
		Data:    log.Fields{"error": errors.New("request failed with " + secret)},
		Time:    time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		Level:   log.ErrorLevel,
		Message: "operation failed",
	}

	formatted, err := (&NbFormatter{}).Format(entry)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	text := string(formatted)
	if !strings.Contains(text, "request failed") {
		t.Fatalf("formatted error is missing: %q", text)
	}
	if strings.Contains(text, secret) {
		t.Fatalf("formatted output leaked secret: %q", text)
	}
}
