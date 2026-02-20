package handlers

import (
	"math/rand"
	"sort"
	"strconv"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pborman/uuid"
)

var allowedCaptchaOptionsCount = map[int]struct{}{
	3:  {},
	4:  {},
	5:  {},
	6:  {},
	8:  {},
	10: {},
}

func normalizeCaptchaOptionsCount(count int) int {
	if _, ok := allowedCaptchaOptionsCount[count]; ok {
		return count
	}
	return captchaSize
}

func (g *Gatekeeper) createCaptchaIndex(lang string) [][2]string {
	vars := g.challengeVariants(lang)
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	captchaIndex := make([][2]string, len(vars))
	for i, key := range keys {
		captchaIndex[i] = [2]string{key, vars[key]}
	}

	return captchaIndex
}

func (g *Gatekeeper) createCaptchaButtons(userID int64, successUUID string, lang string, optionsCount int) ([]api.InlineKeyboardButton, [2]string) {
	captchaIndex := g.createCaptchaIndex(lang)
	if len(captchaIndex) == 0 {
		captchaIndex = g.createCaptchaIndex("en")
	}
	if len(captchaIndex) == 0 {
		fallback := [2]string{"🍎", "apple"}
		return []api.InlineKeyboardButton{
			api.NewInlineKeyboardButtonData(fallback[0], strconv.FormatInt(userID, 10)+";"+successUUID),
		}, fallback
	}

	targetSize := min(len(captchaIndex), normalizeCaptchaOptionsCount(optionsCount))

	captchaRandomSet := make([][2]string, 0, targetSize)
	usedIDs := make(map[int]struct{}, targetSize)
	for len(captchaRandomSet) < targetSize {
		ID := rand.Intn(len(captchaIndex))
		if _, ok := usedIDs[ID]; ok {
			continue
		}
		captchaRandomSet = append(captchaRandomSet, captchaIndex[ID])
		usedIDs[ID] = struct{}{}
	}

	correctVariant := captchaRandomSet[rand.Intn(len(captchaRandomSet))]
	var buttons []api.InlineKeyboardButton
	for _, v := range captchaRandomSet {
		result := strconv.FormatInt(userID, 10) + ";" + uuid.New()
		if v[0] == correctVariant[0] {
			result = strconv.FormatInt(userID, 10) + ";" + successUUID
		}
		buttons = append(buttons, api.NewInlineKeyboardButtonData(v[0], result))
	}

	return buttons, correctVariant
}

func captchaKeyboardRows(buttons []api.InlineKeyboardButton) [][]api.InlineKeyboardButton {
	if len(buttons) == 0 {
		return nil
	}
	switch len(buttons) {
	case 6, 8, 10:
		mid := len(buttons) / 2
		return [][]api.InlineKeyboardButton{
			api.NewInlineKeyboardRow(buttons[:mid]...),
			api.NewInlineKeyboardRow(buttons[mid:]...),
		}
	default:
		return [][]api.InlineKeyboardButton{
			api.NewInlineKeyboardRow(buttons...),
		}
	}
}

func (g *Gatekeeper) challengeVariants(lang string) map[string]string {
	if variants, ok := g.Variants[lang]; ok && len(variants) > 0 {
		return variants
	}
	if variants, ok := g.Variants["en"]; ok && len(variants) > 0 {
		return variants
	}
	return defaultCaptchaVariants
}
