package i18n

import (
	"fmt"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/resources"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

var state = struct {
	translations    map[string]map[string]string
	loaded          map[string]bool
	resourcesPath   string
	defaultLanguage string
}{
	translations:    make(map[string]map[string]string),
	loaded:          make(map[string]bool),
	defaultLanguage: config.Get().DefaultLanguage,
	resourcesPath:   infra.GetResourcesPath("i18n"),
}

func load(lang string) {
	if "en" == lang {
		return
	}

	i18n, err := resources.FS.ReadFile(state.resourcesPath + "/" + fmt.Sprintf("%s.yml", lang))
	if err != nil {
		log.WithError(err).Errorln("cant load i18n")
		return
	}
	translations := make(map[string]string)
	if err := yaml.Unmarshal(i18n, &translations); err != nil {
		log.WithError(err).Errorln("cant unmarshal i18n")
		return
	}
	state.translations[lang] = translations
	state.loaded[lang] = true
}

func Get(key, lang string) string {
	if "en" == lang {
		return key
	}
	if !state.loaded[lang] {
		load(lang)
	}
	if res, ok := state.translations[lang][key]; ok {
		return res
	}
	log.Traceln(`no translation for key "%s"`, key)
	return key
}
