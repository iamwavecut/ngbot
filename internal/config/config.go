package config

import (
	"context"
	"fmt"
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
		Telegram         Telegram
		LLM              LLM
		Reactor          Reactor
		SpamControl      SpamControl
	}

	Telegram struct {
		PollTimeout    time.Duration `env:"TELEGRAM_POLL_TIMEOUT,default=60s"`
		RequestTimeout time.Duration `env:"TELEGRAM_REQUEST_TIMEOUT,default=75s"`
		RecoveryWindow time.Duration `env:"TELEGRAM_RECOVERY_WINDOW,default=10m"`
	}

	LLM struct {
		APIKey  string `env:"LLM_API_KEY,required"`
		Model   string `env:"LLM_API_MODEL,default=gpt-4o-mini"`
		BaseURL string `env:"LLM_API_URL,default=https://api.openai.com/v1"`
		Type    string `env:"LLM_API_TYPE,default=openai"`
	}

	Reactor struct {
		FlaggedEmojis []string `env:"FLAGGED_EMOJIS,default=👎,💩"`
	}

	SpamControl struct {
		LogChannelUsername  string  `env:"SPAM_LOG_CHANNEL_USERNAME"`
		DebugUserID         int64   `env:"SPAM_DEBUG_USER_ID"`
		MinVoters           int     `env:"SPAM_MIN_VOTERS,default=2"`
		MaxVoters           int     `env:"SPAM_MAX_VOTERS,default=10"`
		MinVotersPercentage float64 `env:"SPAM_MIN_VOTERS_PERCENTAGE,default=5"`
		Verbose             bool    `env:"SPAM_VERBOSE,default=false"`

		VotingTimeoutMinutes       time.Duration `env:"SPAM_VOTING_TIMEOUT,default=5m"`
		SuspectNotificationTimeout time.Duration `env:"SPAM_SUSPECT_NOTIFICATION_TIMEOUT,default=2m"`
	}
)

var (
	once         sync.Once
	globalConfig = &Config{}
	globalErr    error
)

func Load() (Config, error) {
	once.Do(func() {
		cfg := &Config{}
		envcfg := envconfig.Config{
			Lookuper: envconfig.PrefixLookuper("NG_", envconfig.OsLookuper()),
			Target:   cfg,
		}
		if err := envconfig.ProcessWith(context.Background(), &envcfg); err != nil {
			globalErr = fmt.Errorf("process env config: %w", err)
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			globalErr = fmt.Errorf("get user home directory: %w", err)
			return
		}
		cfg.DotPath = strings.Replace(cfg.DotPath, "~", home, 1)
		if err := validateConfig(cfg); err != nil {
			globalErr = err
			return
		}
		log.Traceln("loaded config")
		globalConfig = cfg
	})
	return *globalConfig, globalErr
}

func Get() Config {
	cfg, err := Load()
	if err != nil {
		log.WithField("error", err.Error()).Error("cant load config")
	}
	return cfg
}

func validateConfig(cfg *Config) error {
	if cfg.Telegram.PollTimeout <= 0 {
		return fmt.Errorf("telegram poll timeout must be positive")
	}
	if cfg.Telegram.RequestTimeout <= cfg.Telegram.PollTimeout {
		return fmt.Errorf("telegram request timeout must be greater than poll timeout")
	}
	if cfg.Telegram.RecoveryWindow <= cfg.Telegram.RequestTimeout {
		return fmt.Errorf("telegram recovery window must be greater than request timeout")
	}
	return nil
}
