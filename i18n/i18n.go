package i18n

import (
	"fmt"
	"io/ioutil"
	"path"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

var state = struct {
	translations  map[string]map[string]string
	loaded        bool
	resourcesPath string
}{
	translations: make(map[string]map[string]string),
}

func SetResourcesPath(value string) {
	state.resourcesPath = value
}

func load(lang string) {
	state.loaded = true

	if state.resourcesPath == "" {
		state.resourcesPath = "."
	}

	i18n, err := ioutil.ReadFile(path.Join(state.resourcesPath, fmt.Sprintf("%s.yml", lang)))
	if err != nil {
		log.WithError(err).Errorln("cant load i18n")
		return
	}
	if err := yaml.Unmarshal(i18n, state.translations[lang]); err != nil {
		log.WithError(err).Errorln("cant unmarshal i18n")
	}
}

func Get(key, lang string) string {
	if !state.loaded {
		load(lang)
	}

	if res, ok := state.translations[lang][key]; ok {
		return res
	}
	log.Traceln(`no translation for key "%s"`, key)

	return key
}
