package i18n

import (
	"fmt"
	"io/ioutil"
	"path"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

var state = struct {
	language     string
	translations map[string]string
	loaded       bool

	resourcesPath string
}{
	language:     "en",
	translations: make(map[string]string),
}

func SetLanguage(value string) {
	state.language = value
}

func SetResourcesPath(value string) {
	state.resourcesPath = value
}

func load() {
	state.loaded = true

	if state.resourcesPath == "" {
		state.resourcesPath = "."
	}

	i18n, err := ioutil.ReadFile(path.Join(state.resourcesPath, "i18n", fmt.Sprintf("%s.yml", state.language)))
	if err != nil {
		log.WithError(err).Errorln("cant load i18n")
		return
	}
	if err := yaml.Unmarshal(i18n, &state.translations); err != nil {
		log.WithError(err).Errorln("cant unmarshal i18n")
	}
}

func Get(key string) string {
	if !state.loaded {
		load()
	}

	if res, ok := state.translations[key]; ok {
		return res
	}
	log.Traceln(`no translation for key "%s"`, key)

	return key
}
