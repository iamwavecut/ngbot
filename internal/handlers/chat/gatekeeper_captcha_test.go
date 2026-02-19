package handlers

import (
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestCreateCaptchaButtonsFallsBackWhenVariantsMissing(t *testing.T) {
	t.Parallel()

	gk := &Gatekeeper{
		Variants: map[string]map[string]string{},
	}

	buttons, correct := gk.createCaptchaButtons(42, "success", "ru", 5)
	if len(buttons) == 0 {
		t.Fatalf("expected non-empty captcha buttons")
	}
	if correct[0] == "" || correct[1] == "" {
		t.Fatalf("expected non-empty correct variant, got %#v", correct)
	}
}

func TestCreateCaptchaButtonsSupportsSmallVariantSet(t *testing.T) {
	t.Parallel()

	gk := &Gatekeeper{
		Variants: map[string]map[string]string{
			"en": {
				"🍎": "apple",
				"🐶": "dog",
				"🚗": "car",
				"🌟": "star",
				"🎈": "balloon",
			},
			"ru": {
				"🍎": "яблоко",
			},
		},
	}

	buttons, correct := gk.createCaptchaButtons(10, "ok", "ru", 5)
	if len(buttons) < 1 || len(buttons) > captchaSize {
		t.Fatalf("unexpected number of buttons: %d", len(buttons))
	}
	if correct[0] == "" || correct[1] == "" {
		t.Fatalf("expected non-empty correct variant, got %#v", correct)
	}
}

func TestCaptchaKeyboardRowsSplitForLargeSizes(t *testing.T) {
	t.Parallel()

	buttons := make([]api.InlineKeyboardButton, 8)
	for i := range buttons {
		buttons[i] = api.NewInlineKeyboardButtonData("x", "x")
	}

	rows := captchaKeyboardRows(buttons)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if len(rows[0]) != 4 || len(rows[1]) != 4 {
		t.Fatalf("expected rows split by 4/4, got %d/%d", len(rows[0]), len(rows[1]))
	}
}
