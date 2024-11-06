package config

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/sethvargo/go-envconfig"
	log "github.com/sirupsen/logrus"
)

type (
	Config struct {
		TelegramAPIToken string   `env:"TOKEN,required"`
		DefaultLanguage  string   `env:"LANG,default=en"`
		EnabledHandlers  []string `env:"HANDLERS,default=admin,gatekeeper,reactor"`
		LogLevel         int      `env:"LOG_LEVEL,default=2"`
		DotPath          string   `env:"DOT_PATH,default=~/.ngbot"`
		OpenAI           OpenAI
		Reactor          Reactor
	}

	OpenAI struct {
		APIKey  string `env:"OPENAI_API_KEY,required"`
		Model   string `env:"OPENAI_MODEL,default=gpt-4o-mini"`
		BaseURL string `env:"OPENAI_BASE_URL,default=https://api.openai.com/v1"`
	}

	Reactor struct {
		FlaggedEmojis []string `env:"FLAGGED_EMOJIS,default=👎,💩"`
	}
)

var once sync.Once
var globalConfig = &Config{}

func Get() Config {
	once.Do(func() {
		cfg := &Config{}
		envcfg := envconfig.Config{
			Lookuper: envconfig.PrefixLookuper("NG_", envconfig.OsLookuper()),
			Target:   cfg,
		}
		if err := envconfig.ProcessWith(context.Background(), &envcfg); err != nil {
			log.WithError(err).Fatalln("cant load config")

		}
		cfg.DotPath = strings.Replace(cfg.DotPath, "~", os.Getenv("HOME"), 1)
		log.Traceln("loaded config")
		globalConfig = cfg
	})
	return *globalConfig
}
