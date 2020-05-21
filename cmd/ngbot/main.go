package main

import (
	"context"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	"github.com/iamwavecut/ngbot/internal/event"
	"github.com/iamwavecut/ngbot/internal/handlers"
	"os"
	"time"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/jinzhu/configor"
	log "github.com/sirupsen/logrus"
)

func main() {
	// Log as JSON instead of the default ASCII formatter.
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		FullTimestamp:    true,
		TimestampFormat:  "2006-01-02 15:04:05",
		QuoteEmptyFields: true,
	})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.TraceLevel)

	configPath := os.Getenv("NGBOT_CONFIG")
	if configPath == "" {
		configPath = "etc/config.yml"
	}

	cfg := &config.Config{}
	if err := configor.New(&configor.Config{ErrorOnUnmatchedKeys: true}).Load(cfg, configPath); err != nil {
		log.WithError(err).Fatalln("cant load config")
	}
	log.Traceln("loaded config")

	i18n.SetResourcesPath(infra.GetResourcesDir("i18n"))
	i18n.SetDefaultLanguage(cfg.DefaultLanguage)

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	infra.GoRecoverable(-1, "process_updates", func() {
		//rateServer := rates.Get()
		//go rateServer.RunContext(ctx)
		event.RunWorker()

		tgbot, err := tgbotapi.NewBotAPI(cfg.TelegramAPIToken)
		if err != nil {
			log.WithError(err).Errorln("cant initialize bot api")
			time.Sleep(1 * time.Second)
			log.Fatalln("exiting")
		}
		tgbot.Debug = false

		service := bot.NewService(tgbot, sqlite.NewSQLiteClient("bot.db"), cfg)

		bot.RegisterUpdateHandler("admin", handlers.NewAdmin(service))
		bot.RegisterUpdateHandler("gatekeeper", handlers.NewGatekeeper(service))
		bot.RegisterUpdateHandler("charades", handlers.NewCharades(service))

		updateConfig := tgbotapi.NewUpdate(0)
		updateConfig.Timeout = 60
		updateHandler := bot.NewUpdateProcessor(service)

		for update := range tgbot.GetUpdatesChan(updateConfig) {
			if err := updateHandler.Process(&update); err != nil {
				log.WithError(err).Errorln("cant process update")
			}
		}
	})

	select {
	case <-ctx.Done():
		log.WithError(ctx.Err()).Errorln("no more updates")
		os.Exit(0)
	}
}
