package config

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
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
		GatekeeperWebApp GatekeeperWebApp
		LLM              LLM
		SpamControl      SpamControl
	}

	Telegram struct {
		PollTimeout    time.Duration `env:"TELEGRAM_POLL_TIMEOUT,default=60s"`
		RequestTimeout time.Duration `env:"TELEGRAM_REQUEST_TIMEOUT,default=75s"`
		RecoveryWindow time.Duration `env:"TELEGRAM_RECOVERY_WINDOW,default=10m"`
	}

	GatekeeperWebApp struct {
		PublicURL  string `env:"GATEKEEPER_WEBAPP_PUBLIC_URL"`
		ListenAddr string `env:"GATEKEEPER_WEBAPP_LISTEN_ADDR,default=:8080"`
	}

	LLM struct {
		APIKey         string        `env:"LLM_API_KEY,required"`
		Model          string        `env:"LLM_API_MODEL"`
		BaseURL        string        `env:"LLM_API_URL,default=https://api.openai.com/v1"`
		Type           string        `env:"LLM_API_TYPE,default=openai"`
		RequestTimeout time.Duration `env:"LLM_REQUEST_TIMEOUT,default=45s"`
	}

	SpamControl struct {
		LogChannelUsername       string        `env:"SPAM_LOG_CHANNEL_USERNAME"`
		DebugUserID              int64         `env:"SPAM_DEBUG_USER_ID"`
		MinVoters                int           `env:"SPAM_MIN_VOTERS,default=2"`
		MaxVoters                int           `env:"SPAM_MAX_VOTERS,default=10"`
		MinVotersPercentage      float64       `env:"SPAM_MIN_VOTERS_PERCENTAGE,default=5"`
		Verbose                  bool          `env:"SPAM_VERBOSE,default=false"`
		MessageProbationDuration time.Duration `env:"SPAM_MESSAGE_PROBATION_DURATION,default=3h"`

		VotingTimeoutMinutes       time.Duration `env:"SPAM_VOTING_TIMEOUT,default=5m"`
		SuspectNotificationTimeout time.Duration `env:"SPAM_SUSPECT_NOTIFICATION_TIMEOUT,default=2m"`
	}
)

func Load() (Config, error) {
	cfg := &Config{}
	envcfg := envconfig.Config{
		Lookuper: envconfig.PrefixLookuper("NG_", envconfig.OsLookuper()),
		Target:   cfg,
	}
	if err := envconfig.ProcessWith(context.Background(), &envcfg); err != nil {
		return Config{}, fmt.Errorf("process env config: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("get user home directory: %w", err)
	}
	cfg.DotPath = strings.Replace(cfg.DotPath, "~", home, 1)
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	log.Traceln("loaded config")
	return *cfg, nil
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
	if cfg.LLM.RequestTimeout <= 0 {
		return fmt.Errorf("llm request timeout must be positive")
	}
	if cfg.SpamControl.MessageProbationDuration <= 0 {
		return fmt.Errorf("spam message probation duration must be positive")
	}
	if cfg.GatekeeperWebApp.PublicURL != "" {
		parsed, err := url.Parse(cfg.GatekeeperWebApp.PublicURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("gatekeeper web app public url must be an absolute URL")
		}
		if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
			return fmt.Errorf("gatekeeper web app public url must use https unless it points to loopback")
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
