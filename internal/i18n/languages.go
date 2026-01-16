package i18n

import "strings"

var languageNames = map[string]string{
	"be": "Belarusian",
	"bg": "Bulgarian",
	"cs": "Czech",
	"da": "Danish",
	"de": "German",
	"el": "Greek",
	"en": "English",
	"es": "Spanish",
	"et": "Estonian",
	"fi": "Finnish",
	"fr": "French",
	"hu": "Hungarian",
	"id": "Indonesian",
	"it": "Italian",
	"ja": "Japanese",
	"ko": "Korean",
	"lt": "Lithuanian",
	"lv": "Latvian",
	"nb": "Norwegian Bokmal",
	"nl": "Dutch",
	"pl": "Polish",
	"pt": "Portuguese",
	"ro": "Romanian",
	"ru": "Russian",
	"sk": "Slovak",
	"sl": "Slovenian",
	"sv": "Swedish",
	"tr": "Turkish",
	"uk": "Ukrainian",
	"zh": "Chinese",
}

func GetLanguageName(code string) string {
	normalized := strings.ToLower(code)
	if name, ok := languageNames[normalized]; ok {
		return name
	}
	return code
}
