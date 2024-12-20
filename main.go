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

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/gemini"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/openai"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	"github.com/iamwavecut/ngbot/internal/event"
	"github.com/iamwavecut/ngbot/internal/handlers"
	"github.com/iamwavecut/ngbot/internal/i18n"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/internal/observability"
)

func main() {
	cfg := config.Get()
	log.SetFormatter(&config.NbFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.Level(cfg.LogLevel))
	tool.SetLogger(log.StandardLogger())

	maskedConfig := maskConfiguration(&cfg)
	if configJSON, err := json.MarshalIndent(maskedConfig, "", "  "); err != nil {
		log.WithField("error", err.Error()).Error("Failed to marshal config to JSON")
	} else {
		log.WithField("config", string(configJSON)).Debug("Application configuration")
	}

	tool.Try(api.SetLogger(log.WithField("context", "bot_api")), true)
	i18n.Init()

	ctx := context.Background()

	// Initialize observability stack
	if err := observability.Init(ctx); err != nil {
		log.Fatalf("Failed to initialize observability: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	errChan := make(chan error, 1)

	go runBot(ctx, &cfg, errChan)

	shutdown := false
	for !shutdown {
		select {
		case <-infra.MonitorExecutable():
			log.Info("Executable file was modified, initiating shutdown...")
			shutdown = true
		case sig := <-sigChan:
			log.Infof("Received signal %v, initiating shutdown...", sig)
			shutdown = true
		case err := <-errChan:
			log.WithField("error", err.Error()).Error("Bot error occurred")
			shutdown = true
		}
	}

	log.Info("Starting graceful shutdown...")

	// Wait for graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	select {
	case <-shutdownCtx.Done():
		log.Warn("Graceful shutdown timed out, forcing exit")
		os.Exit(1)
	case <-ctx.Done():
		log.Info("Graceful shutdown completed")
	}
}

func maskConfiguration(cfg *config.Config) *config.Config {
	maskedConfig := *cfg
	maskSecret := func(s string) string {
		if len(s) == 0 {
			return s
		}
		visiblePart := len(s) / 5
		return s[:visiblePart] + strings.Repeat("*", len(s)-2*visiblePart) + s[len(s)-visiblePart:]
	}
	maskedConfig.TelegramAPIToken = maskSecret(cfg.TelegramAPIToken)
	maskedConfig.LLM.APIKey = maskSecret(cfg.LLM.APIKey)
	return &maskedConfig
}

func runBot(ctx context.Context, cfg *config.Config, errChan chan<- error) {
	err := tool.Recoverer(-1, func() {
		defer event.RunWorker()()

		// Initialize bot API
		botAPI, err := api.NewBotAPI(cfg.TelegramAPIToken)
		if err != nil {
			log.WithField("error", err.Error()).Error("Failed to initialize bot API")
			errChan <- err
			return
		}
		if log.Level(cfg.LogLevel) == log.TraceLevel {
			botAPI.Debug = true
		}
		defer botAPI.StopReceivingUpdates()

		botAPI.GetMyCommands()

		// commandsScope := api.NewBotCommandScopeAllGroupChats()
		// setMyCommandsConfig := api.NewSetMyCommandsWithScope(commandsScope, api.BotCommand{
		// 	Command:     "ban",
		// 	Description: "Ban user (admin), or start a voting to ban user (all)",
		// })

		// _, err = botAPI.Request(setMyCommandsConfig)
		// if err != nil {
		// 	log.WithField("error", err.Error()).Error("Failed to set my commands")
		// }

		// Initialize services and handlers
		service := bot.NewService(ctx, botAPI, sqlite.NewSQLiteClient(ctx, "bot.db"), log.WithField("context", "service"))
		initializeHandlers(service, cfg, log.WithField("context", "handlers"))

		// Configure updates
		updateConfig := configureUpdates()
		updateProcessor := bot.NewUpdateProcessor(service)

		// Start receiving updates
		updateChan, updateErrChan := bot.GetUpdatesChans(ctx, botAPI, updateConfig)

		// Process updates
		for {
			select {
			case err := <-updateErrChan:
				log.WithField("error", err.Error()).Error("Bot API get updates error")
				errChan <- err
				return
			case update := <-updateChan:
				if err := updateProcessor.Process(ctx, &update); err != nil {
					log.WithField("error", err.Error()).Error("Failed to process update")
				}
			case <-ctx.Done():
				log.Info("Bot shutdown initiated")
				return
			}
		}
	})

	if err != nil {
		errChan <- err
	}
}

func initializeHandlers(service bot.Service, cfg *config.Config, logger *log.Entry) {

	banService := handlers.NewBanService(
		service.GetBot(),
		service.GetDB(),
	)
	spamControl := handlers.NewSpamControl(service, cfg.SpamControl, banService, cfg.SpamControl.Verbose)
	bot.RegisterUpdateHandler("gatekeeper", handlers.NewGatekeeper(service, banService))
	bot.RegisterUpdateHandler("admin", handlers.NewAdmin(service, banService, spamControl))

	llmAPI := configureLLM(cfg, logger)
	spamDetector := handlers.NewSpamDetector(llmAPI, logger.WithField("context", "spam_detector"))

	bot.RegisterUpdateHandler("reactor", handlers.NewReactor(service, banService, spamControl, spamDetector, handlers.Config{
		FlaggedEmojis: cfg.Reactor.FlaggedEmojis,
		OpenAIModel:   cfg.LLM.Model,
		SpamControl:   cfg.SpamControl,
	}))
}

func configureLLM(cfg *config.Config, logger *log.Entry) adapters.LLM {
	switch cfg.LLM.Type {
	case "openai":
		return openai.NewOpenAI(
			cfg.LLM.APIKey,
			cfg.LLM.Model,
			cfg.LLM.BaseURL,
			logger.WithField("context", "llm"),
		)
	case "gemini":
		return gemini.NewGemini(
			cfg.LLM.APIKey,
			cfg.LLM.Model,
			logger.WithField("context", "llm"),
		)
	}
	return nil
}

func configureUpdates() api.UpdateConfig {
	updateConfig := api.NewUpdate(0)
	updateConfig.Timeout = 60
	updateConfig.AllowedUpdates = []string{
		"message", "edited_message", "channel_post", "edited_channel_post",
		"message_reaction", "message_reaction_count", "inline_query",
		"chosen_inline_result", "callback_query", "shipping_query",
		"pre_checkout_query", "poll", "poll_answer", "my_chat_member",
		"chat_member", "chat_join_request",
	}
	return updateConfig
}
