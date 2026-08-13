package handlers

import (
	"strings"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestCreateCaptchaButtonsFallsBackWhenVariantsMissing(t *testing.T) {
	t.Parallel()

	gk := &Gatekeeper{
		Variants: map[string]map[string]string{},
	}

	buttons, visual := gk.createCaptchaButtons(42, "success", "ru", 5)
	if len(buttons) == 0 {
		t.Fatalf("expected non-empty captcha buttons")
	}
	if len(visual.PNG) == 0 || visual.Instruction == "" {
		t.Fatalf("expected visual challenge, got %#v", visual)
	}
}

func TestCaptchaPublicPayloadDoesNotIdentifyOpaqueCorrectChoice(t *testing.T) {
	t.Parallel()

	gk := &Gatekeeper{Variants: map[string]map[string]string{"en": defaultCaptchaVariants}}
	buttons, visual := gk.createCaptchaButtons(42, "server-secret", "en", 5)
	if len(visual.PNG) == 0 || visual.Instruction == "" {
		t.Fatalf("visual challenge = %#v", visual)
	}
	for _, button := range buttons {
		if strings.Contains(visual.Instruction, button.Text) {
			t.Fatalf("public visual payload reveals option %q", button.Text)
		}
	}
	correct := 0
	for _, button := range buttons {
		if button.CallbackData != nil && strings.HasSuffix(*button.CallbackData, ";server-secret") {
			correct++
		}
	}
	if correct != 1 {
		t.Fatalf("opaque server token matches = %d, want 1", correct)
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

	buttons, visual := gk.createCaptchaButtons(10, "ok", "ru", 5)
	if len(buttons) < 1 || len(buttons) > captchaSize {
		t.Fatalf("unexpected number of buttons: %d", len(buttons))
	}
	if len(visual.PNG) == 0 || visual.Instruction == "" {
		t.Fatalf("expected visual challenge, got %#v", visual)
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
