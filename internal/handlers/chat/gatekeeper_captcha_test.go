package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"image/png"
	mathrand "math/rand"
	"strconv"
	"strings"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

type deterministicCaptchaRandom struct {
	random *mathrand.Rand
}

func newDeterministicCaptchaRandom(seed int64) deterministicCaptchaRandom {
	return deterministicCaptchaRandom{random: mathrand.New(mathrand.NewSource(seed))}
}

func (r deterministicCaptchaRandom) Intn(limit int) int {
	return r.random.Intn(limit)
}

func TestVisualCaptchaDefeatsLegacySevenSegmentEquationSolver(t *testing.T) {
	t.Parallel()

	const corpusSize = 256
	solved := 0
	for seed := range corpusSize {
		labels, answer, visual := newVisualCaptchaWithRandom(5, "en", newDeterministicCaptchaRandom(int64(seed)+1))
		legacyAnswer, ok := solveLegacySevenSegmentEquation(visual.PNG)
		if ok && legacyAnswer == answer && containsCaptchaLabel(labels, legacyAnswer) {
			solved++
		}
	}
	if solved > 2 {
		t.Fatalf("legacy fixed-layout equation solver solved %d/%d challenges, want at most 2", solved, corpusSize)
	}
}

func TestVisualCaptchaSeededCorpusVariesFamilyImageAndPayload(t *testing.T) {
	t.Parallel()

	const corpusSize = 192
	images := make(map[[sha256.Size]byte]struct{}, corpusSize)
	payloads := make(map[string]struct{}, corpusSize)
	instructions := make(map[string]struct{}, 3)
	for seed := range corpusSize {
		labels, answer, visual := newVisualCaptchaWithRandom(5, "en", newDeterministicCaptchaRandom(int64(seed)+1000))
		if len(labels) != 5 || !containsCaptchaLabel(labels, answer) {
			t.Fatalf("seed %d produced invalid options: labels=%q answer=%q", seed, labels, answer)
		}
		if _, err := png.Decode(bytes.NewReader(visual.PNG)); err != nil {
			t.Fatalf("seed %d produced invalid PNG: %v", seed, err)
		}
		images[sha256.Sum256(visual.PNG)] = struct{}{}
		payloads[strings.Join(labels, "|")] = struct{}{}
		instructions[visual.Instruction] = struct{}{}
	}
	if len(images) < 190 {
		t.Fatalf("unique images = %d/%d, want at least 190", len(images), corpusSize)
	}
	if len(payloads) < 120 {
		t.Fatalf("unique option payloads = %d/%d, want at least 120", len(payloads), corpusSize)
	}
	if len(instructions) != 3 {
		t.Fatalf("challenge families = %d, want 3", len(instructions))
	}
}

func TestVisualCaptchaDoesNotLeakAnswerRelation(t *testing.T) {
	t.Parallel()

	labels, answer, visual := newVisualCaptchaWithRandom(8, "en", newDeterministicCaptchaRandom(42))
	if strings.Contains(visual.Instruction, answer) || strings.Contains(visual.Instruction, "=") {
		t.Fatalf("instruction leaks answer relation: instruction=%q answer=%q", visual.Instruction, answer)
	}
	for _, chunk := range pngChunkTypes(visual.PNG) {
		if chunk == "tEXt" || chunk == "zTXt" || chunk == "iTXt" {
			t.Fatalf("PNG contains text metadata chunk %q", chunk)
		}
	}

	const successToken = "opaque-server-token"
	options := captchaWebAppOptions(labels, answer, successToken)
	encoded, err := encodeWebAppCaptchaOptions("en", visual.Instruction, options)
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}
	for _, forbidden := range []string{`"answer"`, `"correct"`, `"success"`, `"family"`} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("public payload exposes relation field %s: %s", forbidden, encoded)
		}
	}
	correctTokens := 0
	for _, option := range options {
		if option.ID == successToken {
			correctTokens++
		}
	}
	if correctTokens != 1 {
		t.Fatalf("opaque correct token count = %d, want 1", correctTokens)
	}
}

func TestVisualCaptchaLocalizesSharedInstruction(t *testing.T) {
	t.Parallel()

	_, _, english := newVisualCaptchaWithRandom(5, "en", newDeterministicCaptchaRandom(7))
	_, _, russian := newVisualCaptchaWithRandom(5, "ru", newDeterministicCaptchaRandom(7))
	if english.Instruction == russian.Instruction {
		t.Fatalf("localized instructions are identical: %q", english.Instruction)
	}
	if !strings.Contains(russian.Instruction, "символ") {
		t.Fatalf("Russian instruction is not understandable: %q", russian.Instruction)
	}
}

func containsCaptchaLabel(labels []string, candidate string) bool {
	for _, label := range labels {
		if label == candidate {
			return true
		}
	}
	return false
}

func solveLegacySevenSegmentEquation(encoded []byte) (string, bool) {
	image, err := png.Decode(bytes.NewReader(encoded))
	if err != nil || image.Bounds().Dx() != 260 || image.Bounds().Dy() != 96 {
		return "", false
	}
	digit := func(originX int) (int, bool) {
		points := [7][2]int{{14, 20}, {22, 32}, {42, 32}, {14, 45}, {22, 60}, {42, 60}, {14, 72}}
		mask := byte(0)
		for index, point := range points {
			r, g, b, _ := image.At(originX+point[0], point[1]).RGBA()
			if r+g+b < 3*0x6000 {
				mask |= 1 << index
			}
		}
		masks := map[byte]int{0b1110111: 0, 0b0100100: 1, 0b1011101: 2, 0b1101101: 3, 0b0101110: 4, 0b1101011: 5, 0b1111011: 6, 0b0100101: 7, 0b1111111: 8, 0b1101111: 9}
		value, ok := masks[mask]
		return value, ok
	}
	left, leftOK := digit(20)
	right, rightOK := digit(96)
	if !leftOK || !rightOK {
		return "", false
	}
	return strconv.Itoa(left + right), true
}

func pngChunkTypes(encoded []byte) []string {
	if len(encoded) < 8 {
		return nil
	}
	var chunks []string
	for offset := 8; offset+12 <= len(encoded); {
		length := int(binary.BigEndian.Uint32(encoded[offset : offset+4]))
		if offset+12+length > len(encoded) {
			break
		}
		chunks = append(chunks, string(encoded[offset+4:offset+8]))
		offset += 12 + length
	}
	return chunks
}

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
