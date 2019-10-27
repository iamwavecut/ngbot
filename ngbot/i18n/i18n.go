package i18n

import (
	"fmt"
	"io/ioutil"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

var state = struct {
	language     string
	translations map[string]string
	loaded       bool
}{
	language:     "en",
	translations: make(map[string]string),
}

func SetLanguage(value string) {
	state.language = value
	load()
}
func load() {
	state.loaded = true
	i18n, err := ioutil.ReadFile(fmt.Sprintf("resources/i18n/%s.yml", state.language))
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
