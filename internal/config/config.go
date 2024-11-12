package config

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

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
		SpamControl      SpamControl
	}

	OpenAI struct {
		APIKey  string `env:"OPENAI_API_KEY,required"`
		Model   string `env:"OPENAI_MODEL,default=gpt-4o-mini"`
		BaseURL string `env:"OPENAI_BASE_URL,default=https://api.openai.com/v1"`
	}

	Reactor struct {
		FlaggedEmojis []string `env:"FLAGGED_EMOJIS,default=👎,💩"`
	}

	SpamControl struct {
		LogChannelID        int64   `env:"SPAM_LOG_CHANNEL_ID"`
		MinVoters           int     `env:"SPAM_MIN_VOTERS,default=2"`
		MaxVoters           int     `env:"SPAM_MAX_VOTERS,default=10"`
		MinVotersPercentage float64 `env:"SPAM_MIN_VOTERS_PERCENTAGE,default=5"`
		Verbose             bool    `env:"SPAM_VERBOSE,default=false"`

		VotingTimeoutMinutes       time.Duration `env:"SPAM_VOTING_TIMEOUT,default=5m"`
		SuspectNotificationTimeout time.Duration `env:"SPAM_SUSPECT_NOTIFICATION_TIMEOUT,default=2m"`
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
