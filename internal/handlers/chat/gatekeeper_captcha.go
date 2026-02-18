package handlers

import (
	"math/rand"
	"sort"
	"strconv"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pborman/uuid"
)

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

func (g *Gatekeeper) createCaptchaButtons(userID int64, successUUID string, lang string) ([]api.InlineKeyboardButton, [2]string) {
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

	targetSize := captchaSize
	if len(captchaIndex) < targetSize {
		targetSize = len(captchaIndex)
	}

	captchaRandomSet := make([][2]string, 0, captchaSize)
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

func (g *Gatekeeper) challengeVariants(lang string) map[string]string {
	if variants, ok := g.Variants[lang]; ok && len(variants) >= captchaSize {
		return variants
	}
	if variants, ok := g.Variants["en"]; ok && len(variants) >= captchaSize {
		return variants
	}
	return defaultCaptchaVariants
}
