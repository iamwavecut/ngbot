package config

import (
	"context"
	"sync"

	"github.com/sethvargo/go-envconfig"
	log "github.com/sirupsen/logrus"
)

type Config struct {
	TelegramAPIToken string   `env:"TOKEN,required"`
	DefaultLanguage  string   `env:"LANG,required"`
	EnabledHandlers  []string `env:"HANDLERS,required"`
	LogLevel         int      `env:"LOG_LEVEL,required"`
	DotPath          string   `env:"DOT_PATH,default=/root/.ngbot"`
}

var once sync.Once
var globalConfig = &Config{}

func Get() Config {
	once.Do(func() {
		cfg := &Config{}
		pl := envconfig.PrefixLookuper("NG_", envconfig.OsLookuper())
		if err := envconfig.ProcessWith(context.Background(), cfg, pl); err != nil {
			log.WithError(err).Fatalln("cant load config")

		}
		log.Traceln("loaded config")
		globalConfig = cfg
	})
	return *globalConfig
}
