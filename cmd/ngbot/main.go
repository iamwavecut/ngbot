package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/gemini"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/openai"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	adminHandlers "github.com/iamwavecut/ngbot/internal/handlers/admin"
	chatHandlers "github.com/iamwavecut/ngbot/internal/handlers/chat"
	moderationHandlers "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/internal/lifecycle"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"
)

type updateLoopComponent struct {
	botAPI        *api.BotAPI
	updateConfig  api.UpdateConfig
	updateProcess *bot.UpdateProcessor
	errChan       chan<- error

	cancel context.CancelFunc
	done   chan struct{}
}

func newUpdateLoopComponent(botAPI *api.BotAPI, updateConfig api.UpdateConfig, updateProcess *bot.UpdateProcessor, errChan chan<- error) *updateLoopComponent {
	return &updateLoopComponent{
		botAPI:        botAPI,
		updateConfig:  updateConfig,
		updateProcess: updateProcess,
		errChan:       errChan,
	}
}

func (u *updateLoopComponent) Start(ctx context.Context) error {
	if u.done != nil {
		return nil
	}

	loopCtx, cancel := context.WithCancel(ctx)
	u.cancel = cancel
	u.done = make(chan struct{})

	updateChan, updateErrChan := bot.GetUpdatesChans(loopCtx, u.botAPI, u.updateConfig)
	go func() {
		defer close(u.done)
		for {
			select {
			case <-loopCtx.Done():
				return
			case err, ok := <-updateErrChan:
				if !ok {
					return
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					select {
					case u.errChan <- err:
					default:
						log.WithError(err).Error("update loop error dropped")
					}
				}
				return
			case update, ok := <-updateChan:
				if !ok {
					return
				}
				if err := u.updateProcess.Process(loopCtx, &update); err != nil && !errors.Is(err, context.Canceled) {
					log.WithError(err).Error("Failed to process update")
				}
			}
		}
	}()

	return nil
}

func (u *updateLoopComponent) Stop(ctx context.Context) error {
	if u.cancel != nil {
		u.cancel()
	}
	u.botAPI.StopReceivingUpdates()

	if u.done == nil {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.done:
		u.done = nil
		u.cancel = nil
		return nil
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.WithField("error", err.Error()).Error("cant load config")
		os.Exit(1)
	}

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

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	runtime, err := buildRuntime(rootCtx, &cfg, errChan)
	if err != nil {
		log.WithField("error", err.Error()).Error("Failed to build runtime")
		os.Exit(1)
	}

	if err := runtime.Start(rootCtx); err != nil {
		log.WithField("error", err.Error()).Error("Failed to start runtime")
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	execChanges := infra.MonitorExecutable(rootCtx)

	shutdownReason := ""
	select {
	case <-execChanges:
		shutdownReason = "Executable file was modified, initiating shutdown"
	case sig := <-sigChan:
		shutdownReason = fmt.Sprintf("Received signal %v, initiating shutdown", sig)
	case err := <-errChan:
		shutdownReason = fmt.Sprintf("Runtime error: %v", err)
	}
	log.Info(shutdownReason)

	cancel()
	log.Info("Starting graceful shutdown")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := runtime.Stop(shutdownCtx); err != nil {
		log.WithField("error", err.Error()).Error("Graceful shutdown failed")
		os.Exit(1)
	}

	log.Info("Graceful shutdown completed")
}

func buildRuntime(ctx context.Context, cfg *config.Config, errChan chan<- error) (*lifecycle.Runtime, error) {
	botAPI, err := api.NewBotAPI(cfg.TelegramAPIToken)
	if err != nil {
		return nil, fmt.Errorf("initialize bot API: %w", err)
	}
	if log.Level(cfg.LogLevel) == log.TraceLevel {
		botAPI.Debug = true
	}

	if err := announceGroupAdminCommands(botAPI); err != nil {
		log.WithError(err).Warn("failed to set group admin commands")
	}

	dbClient, err := sqlite.NewSQLiteClient(ctx, cfg.DotPath, "bot.db")
	if err != nil {
		return nil, fmt.Errorf("initialize sqlite client: %w", err)
	}

	service := bot.NewService(ctx, botAPI, dbClient, log.WithField("context", "service"))
	banService := moderationHandlers.NewBanService(service.GetBot(), service.GetDB())
	spamControl := moderationHandlers.NewSpamControl(service, cfg.SpamControl, banService, cfg.SpamControl.Verbose)

	gatekeeperHandler := chatHandlers.NewGatekeeper(service, cfg, banService)
	adminHandler := adminHandlers.NewAdmin(service)

	llmAPI, err := configureLLM(cfg, log.WithField("context", "handlers"))
	if err != nil {
		return nil, fmt.Errorf("configure llm: %w", err)
	}
	spamDetector := moderationHandlers.NewSpamDetector(llmAPI, log.WithField("context", "spam_detector"))

	reactorHandler := chatHandlers.NewReactor(service, banService, spamControl, spamDetector, chatHandlers.Config{
		FlaggedEmojis: cfg.Reactor.FlaggedEmojis,
		OpenAIModel:   cfg.LLM.Model,
		SpamControl:   cfg.SpamControl,
	})

	bot.RegisterUpdateHandler("gatekeeper", gatekeeperHandler)
	bot.RegisterUpdateHandler("admin", adminHandler)
	bot.RegisterUpdateHandler("reactor", reactorHandler)

	updateLoop := newUpdateLoopComponent(botAPI, configureUpdates(), bot.NewUpdateProcessor(service), errChan)

	runtime := lifecycle.NewRuntime(
		service,
		banService,
		spamControl,
		gatekeeperHandler,
		adminHandler,
		updateLoop,
	)
	return runtime, nil
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

func configureLLM(cfg *config.Config, logger *log.Entry) (adapters.LLM, error) {
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
	default:
		return nil, fmt.Errorf("unsupported LLM type %q", cfg.LLM.Type)
	}
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

func announceGroupAdminCommands(botAPI *api.BotAPI) error {
	if _, err := botAPI.Request(api.NewDeleteMyCommands()); err != nil {
		return fmt.Errorf("delete commands: %w", err)
	}

	groupAdminCommands := []api.BotCommand{
		{
			Command:     "settings",
			Description: "Bot settings",
		},
	}

	groupAdminCommandsSet := api.NewSetMyCommandsWithScope(
		api.NewBotCommandScopeAllChatAdministrators(),
		groupAdminCommands...,
	)
	if _, err := botAPI.Request(groupAdminCommandsSet); err != nil {
		return fmt.Errorf("set group admin commands: %w", err)
	}

	return nil
}
