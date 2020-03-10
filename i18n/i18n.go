package i18n

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

var state = struct {
	translations    map[string]map[string]string
	loaded          map[string]bool
	resourcesPath   string
	defaultLanguage string
}{
	translations: make(map[string]map[string]string),
	loaded:       make(map[string]bool),
}

func SetResourcesPath(value string) {
	state.resourcesPath = value
}

func SetDefaultLanguage(value string) {
	state.defaultLanguage = value
	log.Trace("default language ", value)
}

func load(lang string) {
	if state.defaultLanguage == lang {
		return
	}
	if state.resourcesPath == "" {
		state.resourcesPath = "."
	}

	i18n, err := ioutil.ReadFile(filepath.Join(state.resourcesPath, fmt.Sprintf("%s.yml", lang)))
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
	if state.defaultLanguage == lang {
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
