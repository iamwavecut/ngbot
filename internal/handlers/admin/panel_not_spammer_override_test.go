package handlers

import (
	"testing"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestParseNotSpammerReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantType  string
		wantValue string
	}{
		{
			name:      "username with at",
			input:     "@Some_User",
			wantType:  db.NotSpammerMatchTypeUsername,
			wantValue: "some_user",
		},
		{
			name:      "username without at",
			input:     "Some_User",
			wantType:  db.NotSpammerMatchTypeUsername,
			wantValue: "some_user",
		},
		{
			name:      "numeric user id",
			input:     "123456",
			wantType:  db.NotSpammerMatchTypeUserID,
			wantValue: "123456",
		},
		{
			name:      "tme profile",
			input:     "https://t.me/Some_User",
			wantType:  db.NotSpammerMatchTypeUsername,
			wantValue: "some_user",
		},
		{
			name:      "bare tme profile",
			input:     "t.me/Some_User",
			wantType:  db.NotSpammerMatchTypeUsername,
			wantValue: "some_user",
		},
		{
			name:      "telegram me profile",
			input:     "https://telegram.me/Some_User",
			wantType:  db.NotSpammerMatchTypeUsername,
			wantValue: "some_user",
		},
		{
			name:      "tg resolve profile",
			input:     "tg://resolve?domain=Some_User",
			wantType:  db.NotSpammerMatchTypeUsername,
			wantValue: "some_user",
		},
		{
			name:      "tg user id profile",
			input:     "tg://user?id=123456",
			wantType:  db.NotSpammerMatchTypeUserID,
			wantValue: "123456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseNotSpammerReference(tt.input)
			if err != nil {
				t.Fatalf("parseNotSpammerReference returned error: %v", err)
			}
			if got.MatchType != tt.wantType {
				t.Fatalf("unexpected match type: got %q want %q", got.MatchType, tt.wantType)
			}
			if got.MatchValue != tt.wantValue {
				t.Fatalf("unexpected match value: got %q want %q", got.MatchValue, tt.wantValue)
			}
		})
	}
}

func TestParseNotSpammerReferenceRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"   ",
		"https://t.me/c/123/456",
		"tg://resolve",
		"tg://user?id=abc",
		"https://example.com/user",
		"user name",
	}

	for _, input := range inputs {
		if _, err := parseNotSpammerReference(input); err == nil {
			t.Fatalf("expected parseNotSpammerReference(%q) to fail", input)
		}
	}
}
