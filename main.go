package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

	maskSecret := func(s string) string {
		if len(s) == 0 {
			return s
		}
		visiblePart := len(s) / 5
		return s[:visiblePart] + strings.Repeat("*", len(s)-2*visiblePart) + s[len(s)-visiblePart:]
	}

	maskedConfig := cfg
	maskedConfig.TelegramAPIToken = maskSecret(cfg.TelegramAPIToken)
	maskedConfig.OpenAI.APIKey = maskSecret(cfg.OpenAI.APIKey)

	configJSON, err := json.MarshalIndent(maskedConfig, "", "  ")
	if err != nil {
		log.WithError(err).Error("Failed to marshal config to JSON")
	} else {
		log.WithField("config", string(configJSON)).Debug("Application configuration")
	}

	tool.Try(api.SetLogger(log.WithField("context", "bot_api")), true)
	i18n.Init()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		err := tool.Recoverer(-1, func() {
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

			service := bot.NewService(ctx, botAPI, sqlite.NewSQLiteClient("bot.db"), log.WithField("context", "service"))

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
			updateProcessor := bot.NewUpdateProcessor(ctx, service)

			updateChan, errorChan := bot.GetUpdatesChans(ctx, botAPI, updateConfig)

		loop:
			for {
				select {
				case err := <-errorChan:
					log.WithError(err).Errorln("bot api get updates error")
					return
				case update := <-updateChan:
					if err := updateProcessor.Process(&update); err != nil {
						log.WithError(err).Errorln("cant process update")
					}
				case <-ctx.Done():
					log.Info("Shutting down gracefully...")
					break loop
				}
			}
		})
		if err != nil {
			log.WithError(err).Errorln("recoverer exits")
		}
		cancel() // Signal main goroutine to exit
	}()

	select {
	case <-infra.MonitorExecutable():
		log.Info("Executable file was modified, initiating shutdown...")
		cancel()
	case sig := <-sigChan:
		log.Infof("Received signal %v, initiating shutdown...", sig)
		cancel()
	case <-ctx.Done():
		log.Info("Shutdown initiated")
	}

	shutdownTimer := time.NewTimer(10 * time.Second)
	select {
	case <-shutdownTimer.C:
		log.Warn("Graceful shutdown timed out, forcing exit")
		os.Exit(1)
	case <-ctx.Done():
		log.Info("Graceful shutdown completed")
	}
}
