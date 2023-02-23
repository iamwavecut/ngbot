package i18n

import (
	"sort"
	"strings"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/resources"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

var state = struct {
	translations       map[string]map[string]string // [key][lang][translation]
	resourcesPath      string
	defaultLanguage    string
	availableLanguages []string
}{
	translations:    map[string]map[string]string{},
	defaultLanguage: config.Get().DefaultLanguage,
	resourcesPath:   infra.GetResourcesPath("i18n"),
}

func Init() {
	if len(state.translations) > 0 {
		return
	}
	i18n, err := resources.FS.ReadFile(state.resourcesPath + "/translations.yml")
	if err != nil {
		log.WithError(err).Errorln("cant load translations")
		return
	}
	if err := yaml.Unmarshal(i18n, &(state.translations)); err != nil {
		log.WithError(err).Errorln("cant unmarshal translations")
		return
	}
	languages := map[string]struct{}{}
	for _, langs := range state.translations {
		for lang := range langs {
			languages[strings.ToLower(lang)] = struct{}{}
		}
	}
	for lang := range languages {
		state.availableLanguages = append(state.availableLanguages, lang)
	}
	sort.Strings(state.availableLanguages)
	log.Traceln("languages count:", len(state.availableLanguages))
}

func GetLanguagesList() []string {
	return state.availableLanguages[:]
}

func Get(key, lang string) string {
	if "en" == lang {
		return key
	}
	if res, ok := state.translations[key][strings.ToUpper(lang)]; ok {
		return res
	}
	log.Traceln(`no "` + lang + `" translation for key "` + key + `"`)
	return key
}
