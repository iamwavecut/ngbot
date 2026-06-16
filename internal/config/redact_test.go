package config

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	const sampleToken = "123456789:AAEhBOweik6ad9r_ExampleTokenValue12345"

	tests := []struct {
		name        string
		input       string
		wantMasked  []string
		wantPresent []string
		wantEqual   string
	}{
		{
			name:        "telegram token in api url is masked",
			input:       "Post \"https://api.telegram.org/bot" + sampleToken + "/getUpdates\": context deadline exceeded",
			wantMasked:  []string{sampleToken, "AAEhBOweik6ad9r"},
			wantPresent: []string{"bot123456789:", "getUpdates", redactedPlaceholder},
		},
		{
			name:        "telegram token in file url is masked",
			input:       "https://api.telegram.org/file/bot" + sampleToken + "/photos/file.jpg",
			wantMasked:  []string{sampleToken},
			wantPresent: []string{"bot123456789:", redactedPlaceholder},
		},
		{
			name:        "bare telegram token is masked",
			input:       "token=" + sampleToken + " loaded",
			wantMasked:  []string{sampleToken},
			wantPresent: []string{redactedPlaceholder, "loaded"},
		},
		{
			name:      "ordinary text is unchanged",
			input:     "level=info msg=\"Failed to poll updates\" backoff=1s",
			wantEqual: "level=info msg=\"Failed to poll updates\" backoff=1s",
		},
		{
			name:      "timestamp colon is not mistaken for a token",
			input:     "ts=2026-06-16 15:04:05.000 source=polling.go:77",
			wantEqual: "ts=2026-06-16 15:04:05.000 source=polling.go:77",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.input)
			if tt.wantEqual != "" && got != tt.wantEqual {
				t.Fatalf("Redact() = %q, want unchanged %q", got, tt.wantEqual)
			}
			for _, secret := range tt.wantMasked {
				if strings.Contains(got, secret) {
					t.Fatalf("Redact() leaked secret %q in %q", secret, got)
				}
			}
			for _, want := range tt.wantPresent {
				if !strings.Contains(got, want) {
					t.Fatalf("Redact() = %q, missing expected fragment %q", got, want)
				}
			}
		})
	}
}

func TestRedactRegisteredLiteral(t *testing.T) {
	const secret = "llm-secret-key-abcdef-0123456789"
	RegisterSecret(secret)

	got := Redact("LLM_API_KEY=" + secret + " configured")
	if strings.Contains(got, secret) {
		t.Fatalf("Redact() leaked registered literal in %q", got)
	}
	if !strings.Contains(got, redactedPlaceholder) {
		t.Fatalf("Redact() = %q, expected placeholder", got)
	}
	if !strings.Contains(got, "configured") {
		t.Fatalf("Redact() = %q, dropped surrounding text", got)
	}
}

func TestRegisterSecretIgnoresShortValues(t *testing.T) {
	RegisterSecret("short")
	if got := Redact("short story"); got != "short story" {
		t.Fatalf("Redact() masked a short value: %q", got)
	}
}
