package config

type Config struct {
	TelegramAPIToken string `yaml:"telegram_api_token" required:"true"`
	Language         string `yaml:"language" required:"true"`
}
