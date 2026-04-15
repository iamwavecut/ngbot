package handlers

import (
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestNormalizeGreetingTemplateInputEscapesPlainTextAndPreservesPlaceholders(t *testing.T) {
	t.Parallel()

	msg := &api.Message{
		Text: "Hello, {user}! Visit https://t.me/waveclub/42 (now).",
	}

	got, err := normalizeGreetingTemplateInput(msg)
	if err != nil {
		t.Fatalf("normalizeGreetingTemplateInput returned error: %v", err)
	}

	want := db.WrapGatekeeperGreetingMarkdownV2Template(`Hello, {user}\! Visit https://t\.me/waveclub/42 \(now\)\.`)
	if got != want {
		t.Fatalf("unexpected normalized greeting:\n got: %q\nwant: %q", got, want)
	}
}

func TestNormalizeGreetingTemplateInputUsesEntitiesForMarkdownV2(t *testing.T) {
	t.Parallel()

	msg := &api.Message{
		Text: "Read post and greet {user}",
		Entities: []api.MessageEntity{
			{Type: "text_link", Offset: 0, Length: 9, URL: "https://t.me/waveclub/42"},
			{Type: "bold", Offset: 20, Length: 6},
		},
	}

	got, err := normalizeGreetingTemplateInput(msg)
	if err != nil {
		t.Fatalf("normalizeGreetingTemplateInput returned error: %v", err)
	}

	want := db.WrapGatekeeperGreetingMarkdownV2Template(`[Read post](https://t\.me/waveclub/42) and greet *{user}*`)
	if got != want {
		t.Fatalf("unexpected normalized greeting:\n got: %q\nwant: %q", got, want)
	}
}

func TestNormalizeGreetingTemplateInputRejectsPlaceholderInsideLinkEntity(t *testing.T) {
	t.Parallel()

	msg := &api.Message{
		Text: "{user}",
		Entities: []api.MessageEntity{
			{Type: "text_link", Offset: 0, Length: 6, URL: "https://t.me/waveclub/42"},
		},
	}

	if _, err := normalizeGreetingTemplateInput(msg); err == nil {
		t.Fatal("expected placeholder inside text_link to be rejected")
	}
}
