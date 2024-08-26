package main

import (
	"context"
	"os"
	"time"

	"github.com/iamwavecut/tool"
	"github.com/sashabaranov/go-openai"

	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	"github.com/iamwavecut/ngbot/internal/event"
	"github.com/iamwavecut/ngbot/internal/handlers"
	"github.com/iamwavecut/ngbot/internal/i18n"

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
	tool.SetLogger(log.StandardLogger())
	tool.Try(api.SetLogger(log.WithField("context", "bot_api")), true)
	i18n.Init()

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
			llmAPIConfig := openai.DefaultConfig(cfg.OpenAI.APIKey)
			llmAPIConfig.BaseURL = cfg.OpenAI.BaseURL
			llmAPI := openai.NewClientWithConfig(llmAPIConfig)
			bot.RegisterUpdateHandler("reactor", handlers.NewReactor(service, llmAPI, cfg.OpenAI.Model))

			updateConfig := api.NewUpdate(0)
			updateConfig.Timeout = 60
			updateConfig.AllowedUpdates = []string{
				"message",
				"edited_message",
				"channel_post",
				"edited_channel_post",
				"message_reaction",
				"message_reaction_count",
				"inline_query",
				"chosen_inline_result",
				"callback_query",
				"shipping_query",
				"pre_checkout_query",
				"poll",
				"poll_answer",
				"my_chat_member",
				"chat_member",
				"chat_join_request",
			}
			updateProcessor := bot.NewUpdateProcessor(service)

			updateChan, errorChan := bot.GetUpdatesChans(botAPI, updateConfig)

		loop:
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
					break loop
				}
			}
			time.Sleep(time.Second)
		})
		log.WithError(err).Errorln("recoverer exits")
		os.Exit(1)
	}()

	select {
	case <-infra.MonitorExecutable():
		log.Errorln("executable file was modified")
	}
	os.Exit(0)
}
