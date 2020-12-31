package config

type Config struct {
	TelegramAPIToken string   `yaml:"telegram_api_token" required:"true"`
	MaintainerID     int      `yaml:"maintainer_id" required:"true"`
	DefaultLanguage  string   `yaml:"default_language" required:"true"`
	EnabledHandlers  []string `yaml:"enabled_handlers" required:"true"`
}
