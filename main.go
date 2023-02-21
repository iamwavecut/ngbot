package main

import (
	"context"
	"os"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	"github.com/iamwavecut/ngbot/internal/event"
	"github.com/iamwavecut/ngbot/internal/handlers"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/infra"
)

func main() {
	cfg := config.Get()
	log.SetFormatter(&config.NbFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.Level(cfg.LogLevel))
	tool.SetLogger(log.WithField("context", "bot_api"))
	tool.Try(api.SetLogger(log.StandardLogger()), true)

	go func() {
		err := tool.Recoverer(-1, func() {
			ctx, cancelFunc := context.WithCancel(context.Background())
			defer cancelFunc()
			defer event.RunWorker()()
			botAPI, err := api.NewBotAPI(cfg.TelegramAPIToken)
			if err != nil {

				log.WithError(err).Errorln("cant initialize bot api")
				log.Panicln("exiting")
			}
			if log.Level(cfg.LogLevel) == log.TraceLevel {
				botAPI.Debug = true
			}
			defer botAPI.StopReceivingUpdates()

			service := bot.NewService(botAPI, sqlite.NewSQLiteClient("bot.db"))

			bot.RegisterUpdateHandler("admin", handlers.NewAdmin(service))
			bot.RegisterUpdateHandler("gatekeeper", handlers.NewGatekeeper(service))
			bot.RegisterUpdateHandler("punto", handlers.NewPunto(service, infra.GetResourcesPath("punto")))

			updateConfig := api.NewUpdate(0)
			updateConfig.Timeout = 60
			updateProcessor := bot.NewUpdateProcessor(service)

			updateChan, errorChan := bot.GetUpdatesChans(botAPI, updateConfig)

			for {
				select {
				case err := <-errorChan:
					log.WithError(err).Fatalln("bot api get updates error")
				case update := <-updateChan:
					if err := updateProcessor.Process(&update); err != nil {
						log.WithError(err).Errorln("cant process update")
					}
				case <-ctx.Done():
					log.WithError(ctx.Err()).Errorln("no more updates")
					return
				}
			}
			time.Sleep(time.Second)
		})
		log.WithError(err).Errorln("recoverer exited")
		os.Exit(1)
	}()

	select {
	case <-infra.MonitorExecutable():
		log.Errorln("executable file was modified")
	}
	os.Exit(0)
}
