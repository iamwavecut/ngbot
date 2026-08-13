package handlers

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/big"
	"strconv"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pborman/uuid"
)

var allowedCaptchaOptionsCount = map[int]struct{}{
	3: {}, 4: {}, 5: {}, 6: {}, 8: {}, 10: {},
}

var relationalCaptchaGlyphs = []string{"●", "▲", "■", "★", "✚", "✕", "⬟", "⬢", "♥", "☾"}

var relationalCaptchaInstructions = map[string][3]string{
	"en": {
		"Select the symbol that appears exactly twice in the image.",
		"Select the symbol inside the double ring.",
		"Select the symbol joined to its twin by a line.",
	},
	"ru": {
		"Выберите символ, который встречается на изображении ровно два раза.",
		"Выберите символ внутри двойного кольца.",
		"Выберите символ, соединённый линией со своей копией.",
	},
}

var relationalCaptchaPalettes = []struct {
	background color.RGBA
	ink        []color.RGBA
	noise      color.RGBA
	marker     color.RGBA
}{
	{color.RGBA{R: 247, G: 244, B: 238, A: 255}, []color.RGBA{{R: 25, G: 61, B: 77, A: 255}, {R: 121, G: 47, B: 61, A: 255}, {R: 41, G: 91, B: 72, A: 255}, {R: 91, G: 57, B: 133, A: 255}}, color.RGBA{R: 185, G: 174, B: 157, A: 255}, color.RGBA{R: 189, G: 75, B: 48, A: 255}},
	{color.RGBA{R: 237, G: 244, B: 246, A: 255}, []color.RGBA{{R: 18, G: 59, B: 92, A: 255}, {R: 116, G: 51, B: 37, A: 255}, {R: 35, G: 91, B: 86, A: 255}, {R: 87, G: 52, B: 122, A: 255}}, color.RGBA{R: 158, G: 180, B: 185, A: 255}, color.RGBA{R: 204, G: 91, B: 36, A: 255}},
	{color.RGBA{R: 244, G: 240, B: 248, A: 255}, []color.RGBA{{R: 49, G: 45, B: 94, A: 255}, {R: 128, G: 43, B: 76, A: 255}, {R: 29, G: 92, B: 83, A: 255}, {R: 113, G: 71, B: 25, A: 255}}, color.RGBA{R: 181, G: 168, B: 190, A: 255}, color.RGBA{R: 198, G: 73, B: 88, A: 255}},
}

type captchaRandom interface {
	Intn(limit int) int
}

type secureCaptchaRandom struct{}

type captchaVisual struct {
	Instruction string
	PNG         []byte
}

type captchaPoint struct {
	x float64
	y float64
}

type captchaGlyphPlacement struct {
	kind     int
	center   captchaPoint
	scale    float64
	rotation float64
	style    int
	ink      color.RGBA
}

func (secureCaptchaRandom) Intn(limit int) int {
	if limit <= 0 {
		panic("captcha random limit must be positive")
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		panic(fmt.Sprintf("captcha randomness unavailable: %v", err))
	}
	return int(value.Int64())
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

func (g *Gatekeeper) createCaptchaButtons(userID int64, successUUID, language string, optionsCount int) ([]api.InlineKeyboardButton, captchaVisual) {
	labels, answer, visual := newVisualCaptchaForLocale(normalizeCaptchaOptionsCount(optionsCount), language)
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
	return newVisualCaptchaForLocale(optionsCount, "en")
}

func newVisualCaptchaForLocale(optionsCount int, language string) ([]string, string, captchaVisual) {
	return newVisualCaptchaWithRandom(optionsCount, language, secureCaptchaRandom{})
}

func newVisualCaptchaWithRandom(optionsCount int, language string, random captchaRandom) ([]string, string, captchaVisual) {
	optionsCount = normalizeCaptchaOptionsCount(optionsCount)
	glyphIndexes := make([]int, len(relationalCaptchaGlyphs))
	for index := range glyphIndexes {
		glyphIndexes[index] = index
	}
	shuffleCaptchaValues(glyphIndexes, random)
	glyphIndexes = glyphIndexes[:optionsCount]
	answerKind := glyphIndexes[random.Intn(len(glyphIndexes))]
	family := random.Intn(3)

	labels := make([]string, 0, len(glyphIndexes))
	for _, glyphIndex := range glyphIndexes {
		labels = append(labels, relationalCaptchaGlyphs[glyphIndex])
	}
	shuffleCaptchaValues(labels, random)

	instructions := relationalCaptchaInstructions[normalizeCaptchaInstructionLocale(language)]
	return labels, relationalCaptchaGlyphs[answerKind], captchaVisual{
		Instruction: instructions[family],
		PNG:         renderRelationalCaptcha(glyphIndexes, answerKind, family, random),
	}
}

func normalizeCaptchaInstructionLocale(language string) string {
	normalized := strings.ToLower(strings.TrimSpace(language))
	normalized, _, _ = strings.Cut(normalized, "-")
	if normalized == "ru" {
		return "ru"
	}
	return "en"
}

func shuffleCaptchaValues[T any](values []T, random captchaRandom) {
	for index := len(values) - 1; index > 0; index-- {
		other := random.Intn(index + 1)
		values[index], values[other] = values[other], values[index]
	}
}

func renderRelationalCaptcha(optionKinds []int, answerKind, family int, random captchaRandom) []byte {
	width := 344 + random.Intn(37)
	height := 164 + random.Intn(25)
	palette := relationalCaptchaPalettes[random.Intn(len(relationalCaptchaPalettes))]
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	fillCaptchaBackground(canvas, palette.background, random)
	drawCaptchaNoise(canvas, palette.noise, random)

	kinds := append([]int(nil), optionKinds...)
	if family == 0 || family == 2 {
		kinds = append(kinds, answerKind)
	}
	shuffleCaptchaValues(kinds, random)
	placements := captchaPlacements(width, height, kinds, palette.ink, random)

	switch family {
	case 1:
		for _, placement := range placements {
			if placement.kind == answerKind {
				drawCaptchaRing(canvas, placement.center, placement.scale+10, palette.marker, 3)
				drawCaptchaRing(canvas, placement.center, placement.scale+16, palette.marker, 2)
				break
			}
		}
	case 2:
		var twins []captchaGlyphPlacement
		for _, placement := range placements {
			if placement.kind == answerKind {
				twins = append(twins, placement)
			}
		}
		if len(twins) == 2 {
			drawCaptchaLine(canvas, twins[0].center, twins[1].center, palette.marker, 4)
			drawCaptchaRing(canvas, midpointCaptchaPoint(twins[0].center, twins[1].center), 5, palette.marker, 2)
		}
	}

	for _, placement := range placements {
		drawRelationalCaptchaGlyph(canvas, placement)
	}

	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		panic(fmt.Sprintf("encode captcha image: %v", err))
	}
	return output.Bytes()
}

func fillCaptchaBackground(canvas *image.RGBA, background color.RGBA, random captchaRandom) {
	for y := range canvas.Bounds().Dy() {
		for x := range canvas.Bounds().Dx() {
			canvas.SetRGBA(x, y, background)
		}
	}
	for range 260 + random.Intn(121) {
		variation := random.Intn(13) - 6
		x := random.Intn(canvas.Bounds().Dx())
		y := random.Intn(canvas.Bounds().Dy())
		canvas.SetRGBA(x, y, color.RGBA{
			R: uint8(min(max(int(background.R)+variation, 0), 255)),
			G: uint8(min(max(int(background.G)+variation, 0), 255)),
			B: uint8(min(max(int(background.B)+variation, 0), 255)),
			A: 255,
		})
	}
}

func drawCaptchaNoise(canvas *image.RGBA, noise color.RGBA, random captchaRandom) {
	width := canvas.Bounds().Dx()
	height := canvas.Bounds().Dy()
	for range 8 + random.Intn(7) {
		from := captchaPoint{x: float64(random.Intn(width)), y: float64(random.Intn(height))}
		to := captchaPoint{x: float64(random.Intn(width)), y: float64(random.Intn(height))}
		drawCaptchaLine(canvas, from, to, noise, 1+random.Intn(2))
	}
	for range 130 + random.Intn(111) {
		x := random.Intn(width)
		y := random.Intn(height)
		radius := 1 + random.Intn(2)
		for py := max(0, y-radius); py <= min(height-1, y+radius); py++ {
			for px := max(0, x-radius); px <= min(width-1, x+radius); px++ {
				canvas.SetRGBA(px, py, noise)
			}
		}
	}
}

func captchaPlacements(width, height int, kinds []int, inks []color.RGBA, random captchaRandom) []captchaGlyphPlacement {
	columns := 4
	if len(kinds) <= 6 {
		columns = 3
	}
	rows := (len(kinds) + columns - 1) / columns
	cellWidth := float64(width-36) / float64(columns)
	cellHeight := float64(height-30) / float64(rows)
	placements := make([]captchaGlyphPlacement, 0, len(kinds))
	for index, kind := range kinds {
		column := index % columns
		row := index / columns
		center := captchaPoint{
			x: 18 + (float64(column)+0.5)*cellWidth + float64(random.Intn(17)-8),
			y: 15 + (float64(row)+0.5)*cellHeight + float64(random.Intn(13)-6),
		}
		scaleLimit := int(min(cellWidth, cellHeight) * 0.25)
		scale := float64(max(15, scaleLimit-4+random.Intn(9)))
		placements = append(placements, captchaGlyphPlacement{
			kind:     kind,
			center:   center,
			scale:    scale,
			rotation: float64(random.Intn(25)-12) * math.Pi / 180,
			style:    random.Intn(3),
			ink:      inks[(kind+random.Intn(len(inks)))%len(inks)],
		})
	}
	shuffleCaptchaValues(placements, random)
	return placements
}

func drawRelationalCaptchaGlyph(canvas *image.RGBA, placement captchaGlyphPlacement) {
	if placement.kind == 0 {
		drawCaptchaCircleGlyph(canvas, placement)
		return
	}
	if placement.kind == 5 {
		drawCaptchaCrossGlyph(canvas, placement)
		return
	}

	points := captchaGlyphPoints(placement.kind)
	transformed := make([]captchaPoint, 0, len(points))
	for _, point := range points {
		transformed = append(transformed, transformCaptchaPoint(point, placement))
	}
	if placement.style != 1 {
		fillCaptchaPolygon(canvas, transformed, placement.ink)
	}
	drawCaptchaPolygon(canvas, transformed, placement.ink, 2+placement.style)
	if placement.style == 2 && placement.kind != 9 {
		drawCaptchaRing(canvas, placement.center, max(3, placement.scale/5), placement.ink, 2)
	}
}

func captchaGlyphPoints(kind int) []captchaPoint {
	switch kind {
	case 1:
		return regularCaptchaPolygon(3, -math.Pi/2)
	case 2:
		return regularCaptchaPolygon(4, math.Pi/4)
	case 3:
		points := make([]captchaPoint, 0, 10)
		for index := range 10 {
			radius := 1.0
			if index%2 == 1 {
				radius = 0.43
			}
			angle := -math.Pi/2 + float64(index)*math.Pi/5
			points = append(points, captchaPoint{x: math.Cos(angle) * radius, y: math.Sin(angle) * radius})
		}
		return points
	case 4:
		return []captchaPoint{{x: -0.28, y: -1}, {x: 0.28, y: -1}, {x: 0.28, y: -0.28}, {x: 1, y: -0.28}, {x: 1, y: 0.28}, {x: 0.28, y: 0.28}, {x: 0.28, y: 1}, {x: -0.28, y: 1}, {x: -0.28, y: 0.28}, {x: -1, y: 0.28}, {x: -1, y: -0.28}, {x: -0.28, y: -0.28}}
	case 6:
		return regularCaptchaPolygon(5, -math.Pi/2)
	case 7:
		return regularCaptchaPolygon(6, 0)
	case 8:
		return []captchaPoint{{x: 0, y: 0.95}, {x: -0.92, y: 0.05}, {x: -0.78, y: -0.55}, {x: -0.38, y: -0.88}, {x: 0, y: -0.5}, {x: 0.38, y: -0.88}, {x: 0.78, y: -0.55}, {x: 0.92, y: 0.05}}
	case 9:
		return []captchaPoint{{x: -0.65, y: -0.95}, {x: 0.2, y: -0.72}, {x: 0.78, y: -0.15}, {x: 0.72, y: 0.55}, {x: 0.1, y: 0.92}, {x: -0.65, y: 0.72}, {x: -0.2, y: 0.38}, {x: 0.08, y: -0.05}, {x: -0.08, y: -0.5}}
	default:
		return regularCaptchaPolygon(4, 0)
	}
}

func regularCaptchaPolygon(sides int, offset float64) []captchaPoint {
	points := make([]captchaPoint, 0, sides)
	for index := range sides {
		angle := offset + float64(index)*2*math.Pi/float64(sides)
		points = append(points, captchaPoint{x: math.Cos(angle), y: math.Sin(angle)})
	}
	return points
}

func transformCaptchaPoint(point captchaPoint, placement captchaGlyphPlacement) captchaPoint {
	cosine := math.Cos(placement.rotation)
	sine := math.Sin(placement.rotation)
	x := point.x * placement.scale
	y := point.y * placement.scale
	return captchaPoint{
		x: placement.center.x + x*cosine - y*sine,
		y: placement.center.y + x*sine + y*cosine,
	}
}

func drawCaptchaCircleGlyph(canvas *image.RGBA, placement captchaGlyphPlacement) {
	if placement.style != 1 {
		for y := int(placement.center.y - placement.scale); y <= int(placement.center.y+placement.scale); y++ {
			for x := int(placement.center.x - placement.scale); x <= int(placement.center.x+placement.scale); x++ {
				distance := math.Hypot(float64(x)-placement.center.x, float64(y)-placement.center.y)
				if distance <= placement.scale {
					setCaptchaPixel(canvas, x, y, placement.ink)
				}
			}
		}
	}
	drawCaptchaRing(canvas, placement.center, placement.scale, placement.ink, 2+placement.style)
	if placement.style == 2 {
		drawCaptchaRing(canvas, placement.center, placement.scale*0.32, placement.ink, 2)
	}
}

func drawCaptchaCrossGlyph(canvas *image.RGBA, placement captchaGlyphPlacement) {
	distance := placement.scale * 0.72
	cosine := math.Cos(placement.rotation)
	sine := math.Sin(placement.rotation)
	vector := func(x, y float64) captchaPoint {
		return captchaPoint{x: placement.center.x + x*cosine - y*sine, y: placement.center.y + x*sine + y*cosine}
	}
	thickness := 4 + placement.style
	drawCaptchaLine(canvas, vector(-distance, -distance), vector(distance, distance), placement.ink, thickness)
	drawCaptchaLine(canvas, vector(distance, -distance), vector(-distance, distance), placement.ink, thickness)
}

func drawCaptchaPolygon(canvas *image.RGBA, points []captchaPoint, ink color.RGBA, thickness int) {
	for index, point := range points {
		drawCaptchaLine(canvas, point, points[(index+1)%len(points)], ink, thickness)
	}
}

func fillCaptchaPolygon(canvas *image.RGBA, points []captchaPoint, ink color.RGBA) {
	minimumX, maximumX := points[0].x, points[0].x
	minimumY, maximumY := points[0].y, points[0].y
	for _, point := range points[1:] {
		minimumX = min(minimumX, point.x)
		maximumX = max(maximumX, point.x)
		minimumY = min(minimumY, point.y)
		maximumY = max(maximumY, point.y)
	}
	for y := int(math.Floor(minimumY)); y <= int(math.Ceil(maximumY)); y++ {
		for x := int(math.Floor(minimumX)); x <= int(math.Ceil(maximumX)); x++ {
			if captchaPointInsidePolygon(captchaPoint{x: float64(x) + 0.5, y: float64(y) + 0.5}, points) {
				setCaptchaPixel(canvas, x, y, ink)
			}
		}
	}
}

func captchaPointInsidePolygon(point captchaPoint, polygon []captchaPoint) bool {
	inside := false
	previous := len(polygon) - 1
	for current := range polygon {
		currentPoint := polygon[current]
		previousPoint := polygon[previous]
		crosses := currentPoint.y > point.y != (previousPoint.y > point.y)
		if crosses && point.x < (previousPoint.x-currentPoint.x)*(point.y-currentPoint.y)/(previousPoint.y-currentPoint.y)+currentPoint.x {
			inside = !inside
		}
		previous = current
	}
	return inside
}

func drawCaptchaLine(canvas *image.RGBA, from, to captchaPoint, ink color.RGBA, thickness int) {
	distance := max(math.Abs(to.x-from.x), math.Abs(to.y-from.y))
	steps := max(1, int(math.Ceil(distance)))
	radius := max(0, thickness/2)
	for step := range steps + 1 {
		ratio := float64(step) / float64(steps)
		x := int(math.Round(from.x + (to.x-from.x)*ratio))
		y := int(math.Round(from.y + (to.y-from.y)*ratio))
		for py := y - radius; py <= y+radius; py++ {
			for px := x - radius; px <= x+radius; px++ {
				setCaptchaPixel(canvas, px, py, ink)
			}
		}
	}
}

func drawCaptchaRing(canvas *image.RGBA, center captchaPoint, radius float64, ink color.RGBA, thickness int) {
	steps := max(36, int(radius*5))
	var previous captchaPoint
	for step := range steps + 1 {
		angle := float64(step) * 2 * math.Pi / float64(steps)
		current := captchaPoint{x: center.x + math.Cos(angle)*radius, y: center.y + math.Sin(angle)*radius}
		if step > 0 {
			drawCaptchaLine(canvas, previous, current, ink, thickness)
		}
		previous = current
	}
}

func setCaptchaPixel(canvas *image.RGBA, x, y int, ink color.RGBA) {
	if image.Pt(x, y).In(canvas.Bounds()) {
		canvas.SetRGBA(x, y, ink)
	}
}

func midpointCaptchaPoint(left, right captchaPoint) captchaPoint {
	return captchaPoint{x: (left.x + right.x) / 2, y: (left.y + right.y) / 2}
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
