package handlers

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"strconv"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pborman/uuid"
)

var allowedCaptchaOptionsCount = map[int]struct{}{
	3: {}, 4: {}, 5: {}, 6: {}, 8: {}, 10: {},
}

type captchaVisual struct {
	Instruction string
	PNG         []byte
}

func (v captchaVisual) dataURL() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(v.PNG)
}

func normalizeCaptchaOptionsCount(count int) int {
	if _, ok := allowedCaptchaOptionsCount[count]; ok {
		return count
	}
	return captchaSize
}

func (g *Gatekeeper) createCaptchaButtons(userID int64, successUUID string, _ string, optionsCount int) ([]api.InlineKeyboardButton, captchaVisual) {
	labels, answer, visual := newVisualCaptcha(normalizeCaptchaOptionsCount(optionsCount))
	buttons := make([]api.InlineKeyboardButton, 0, len(labels))
	for _, label := range labels {
		result := uuid.New()
		if label == answer {
			result = successUUID
		}
		buttons = append(buttons, api.NewInlineKeyboardButtonData(label, strconv.FormatInt(userID, 10)+";"+result))
	}
	return buttons, visual
}

func newVisualCaptcha(optionsCount int) ([]string, string, captchaVisual) {
	left := secureCaptchaInt(2, 9)
	right := secureCaptchaInt(2, 9)
	answerValue := left + right
	answer := strconv.Itoa(answerValue)
	values := map[int]struct{}{answerValue: {}}
	for len(values) < optionsCount {
		candidate := secureCaptchaInt(max(1, answerValue-optionsCount), answerValue+optionsCount)
		values[candidate] = struct{}{}
	}
	labels := make([]string, 0, len(values))
	for value := range values {
		labels = append(labels, strconv.Itoa(value))
	}
	secureShuffle(labels)
	return labels, answer, captchaVisual{
		Instruction: "Solve the expression shown in the image and select its result.",
		PNG:         renderCaptchaExpression(left, right),
	}
}

func secureCaptchaInt(minimum, maximum int) int {
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(maximum-minimum+1)))
	if err != nil {
		panic(fmt.Sprintf("captcha randomness unavailable: %v", err))
	}
	return minimum + int(value.Int64())
}

func secureShuffle(values []string) {
	for i := len(values) - 1; i > 0; i-- {
		j := secureCaptchaInt(0, i)
		values[i], values[j] = values[j], values[i]
	}
}

func renderCaptchaExpression(left, right int) []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, 260, 96))
	background := color.RGBA{R: 245, G: 247, B: 250, A: 255}
	for y := range canvas.Bounds().Dy() {
		for x := range canvas.Bounds().Dx() {
			canvas.Set(x, y, background)
		}
	}
	ink := color.RGBA{R: 24, G: 24, B: 27, A: 255}
	x := 20
	for _, char := range strconv.Itoa(left) + "+" + strconv.Itoa(right) + "=?" {
		drawCaptchaGlyph(canvas, x, 18, char, ink)
		x += 38
	}
	for range 28 {
		x := secureCaptchaInt(0, canvas.Bounds().Dx()-1)
		y := secureCaptchaInt(0, canvas.Bounds().Dy()-1)
		canvas.Set(x, y, color.RGBA{R: 120, G: 130, B: 145, A: 120})
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		panic(fmt.Sprintf("encode captcha image: %v", err))
	}
	return output.Bytes()
}

func drawCaptchaGlyph(dst *image.RGBA, x, y int, char rune, ink color.Color) {
	segments := map[rune][]int{
		'0': {0, 1, 2, 4, 5, 6}, '1': {2, 5}, '2': {0, 2, 3, 4, 6},
		'3': {0, 2, 3, 5, 6}, '4': {1, 2, 3, 5}, '5': {0, 1, 3, 5, 6},
		'6': {0, 1, 3, 4, 5, 6}, '7': {0, 2, 5}, '8': {0, 1, 2, 3, 4, 5, 6},
		'9': {0, 1, 2, 3, 5, 6}, '+': {7, 8}, '=': {9, 10}, '?': {0, 2, 3, 8},
	}
	lines := [][4]int{{4, 0, 24, 5}, {0, 4, 5, 25}, {24, 4, 29, 25}, {4, 25, 24, 30}, {0, 29, 5, 50}, {24, 29, 29, 50}, {4, 50, 24, 55}, {12, 13, 17, 43}, {0, 25, 29, 30}, {3, 18, 26, 23}, {3, 34, 26, 39}}
	for _, segment := range segments[char] {
		rect := lines[segment]
		for py := y + rect[1]; py < y+rect[3]; py++ {
			for px := x + rect[0]; px < x+rect[2]; px++ {
				dst.Set(px, py, ink)
			}
		}
	}
}

func captchaKeyboardRows(buttons []api.InlineKeyboardButton) [][]api.InlineKeyboardButton {
	if len(buttons) == 0 {
		return nil
	}
	switch len(buttons) {
	case 6, 8, 10:
		mid := len(buttons) / 2
		return [][]api.InlineKeyboardButton{api.NewInlineKeyboardRow(buttons[:mid]...), api.NewInlineKeyboardRow(buttons[mid:]...)}
	default:
		return [][]api.InlineKeyboardButton{api.NewInlineKeyboardRow(buttons...)}
	}
}
