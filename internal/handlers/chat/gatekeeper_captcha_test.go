package handlers

import "testing"

func TestCreateCaptchaButtonsFallsBackWhenVariantsMissing(t *testing.T) {
	t.Parallel()

	gk := &Gatekeeper{
		Variants: map[string]map[string]string{},
	}

	buttons, correct := gk.createCaptchaButtons(42, "success", "ru")
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

	buttons, correct := gk.createCaptchaButtons(10, "ok", "ru")
	if len(buttons) < 1 || len(buttons) > captchaSize {
		t.Fatalf("unexpected number of buttons: %d", len(buttons))
	}
	if correct[0] == "" || correct[1] == "" {
		t.Fatalf("expected non-empty correct variant, got %#v", correct)
	}
}
