package main

import (
	"context"
	"os"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/config"
	"github.com/iamwavecut/ngbot/infra"
	"github.com/iamwavecut/ngbot/ngbot"
	"github.com/iamwavecut/ngbot/ngbot/handlers"
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

	var cfg config.Config
	if err := configor.New(&configor.Config{ErrorOnUnmatchedKeys: true}).Load(&cfg, "etc/config.yml"); err != nil {
		log.WithError(err).Fatalln("cant load config")
	}

	log.Traceln("loaded config", spew.Sdump(cfg))

	ctx, cancelFunc := context.WithCancel(context.Background())
	infra.GoRecoverable(-1, "getUpdates", func() {
		defer cancelFunc()

		bot, err := tgbotapi.NewBotAPI(cfg.TelegramAPIToken)
		if err != nil {
			log.WithError(err).Errorln("cant initialize bot api")
			time.Sleep(1 * time.Second)
			log.Fatalln("exiting")
		}
		bot.Debug = true

		updateConfig := tgbotapi.NewUpdate(0)
		updateConfig.Timeout = 60
		updateHandler := ngbot.NewUpdateProcessor(&cfg, bot)

		gatekeeper := handlers.NewGatekeeper(&cfg, bot)
		updateHandler.AddHandler(gatekeeper)

		for update := range bot.GetUpdatesChan(updateConfig) {
			if err := updateHandler.Process(update); err != nil {
				log.WithError(err).Errorln("cant process update")
			}
		}
	})

	select {
	case <-ctx.Done():
		log.WithError(ctx.Err()).Errorln("no more updates")
	}
}
