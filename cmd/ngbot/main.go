package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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

const (
	handlerAdmin                    = "admin"
	handlerGatekeeper               = "gatekeeper"
	handlerReactor                  = "reactor"
	redactedConfigurationValue      = "[REDACTED]"
	voteBanCommand                  = "voteban"
	voteBanCommandDescription       = "Report spam with community voting"
	privateHelpCommand              = "help"
	privateHelpCommandDescription   = "Show bot help"
	adminSettingsCommand            = "settings"
	adminSettingsCommandDescription = "Bot settings"
)

type updateLoopComponent struct {
	botAPI       *api.BotAPI
	updateConfig api.UpdateConfig
	polling      bot.PollingOptions
	dispatcher   *bot.KeyedDispatcher
	errChan      chan<- shutdownSignal

	cancel context.CancelFunc
	done   chan struct{}
}

type shutdownSignal struct {
	message  string
	exitCode int
}

func newUpdateLoopComponent(botAPI *api.BotAPI, updateConfig api.UpdateConfig, polling bot.PollingOptions, updateProcess *bot.UpdateProcessor, errChan chan<- shutdownSignal) *updateLoopComponent {
	return &updateLoopComponent{
		botAPI:       botAPI,
		updateConfig: updateConfig,
		polling:      polling,
		dispatcher: bot.NewKeyedDispatcher(
			updateProcess.Process,
			8,
			botAPI.Buffer,
			log.WithField("context", "update_dispatcher"),
		),
		errChan: errChan,
	}
}

func (u *updateLoopComponent) Start(ctx context.Context) error {
	if u.done != nil {
		return nil
	}

	loopCtx, cancel := context.WithCancel(ctx)
	u.cancel = cancel
	u.done = make(chan struct{})
	if err := u.dispatcher.Start(loopCtx); err != nil {
		cancel()
		u.cancel = nil
		u.done = nil
		return fmt.Errorf("start update dispatcher: %w", err)
	}

	updateChan, updateErrChan := bot.GetUpdatesChans(loopCtx, u.botAPI, u.updateConfig, u.polling)
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
					case u.errChan <- shutdownSignal{
						message:  formatPollingShutdown(err),
						exitCode: 1,
					}:
					default:
						log.WithError(err).Error("update loop error dropped")
					}
				}
				return
			case update, ok := <-updateChan:
				if !ok {
					return
				}
				if err := u.dispatcher.Submit(loopCtx, update); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, bot.ErrDispatcherClosed) {
						return
					}
					log.WithError(err).Error("Failed to dispatch update")
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
	dispatcherErr := u.dispatcher.Stop(ctx)

	if u.done == nil {
		return dispatcherErr
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.done:
		u.done = nil
		u.cancel = nil
		return dispatcherErr
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.WithField("error", err.Error()).Error("cant load config")
		os.Exit(1)
	}

	config.RegisterSecret(cfg.TelegramAPIToken)
	config.RegisterSecret(cfg.LLM.APIKey)

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

	i18n.Init()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownChan := make(chan shutdownSignal, 1)
	runtime, err := buildRuntime(rootCtx, &cfg, shutdownChan)
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
	execChanges, err := infra.MonitorExecutable(rootCtx)
	if err != nil {
		log.WithError(err).Warn("Executable monitoring is unavailable")
	}

	shutdown := shutdownSignal{}
	select {
	case <-execChanges:
		shutdown.message = "Executable file was modified, initiating shutdown"
	case sig := <-sigChan:
		shutdown.message = fmt.Sprintf("Received signal %v, initiating shutdown", sig)
	case shutdown = <-shutdownChan:
	}
	log.Info(shutdown.message)

	cancel()
	log.Info("Starting graceful shutdown")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := runtime.Stop(shutdownCtx); err != nil {
		log.WithField("error", err.Error()).Error("Graceful shutdown failed")
		os.Exit(1)
	}

	log.Info("Graceful shutdown completed")
	if shutdown.exitCode != 0 {
		os.Exit(shutdown.exitCode)
	}
}

func buildRuntime(ctx context.Context, cfg *config.Config, errChan chan<- shutdownSignal) (*lifecycle.Runtime, error) {
	botAPI, err := api.NewBotAPIWithOptions(
		cfg.TelegramAPIToken,
		api.WithAPIEndpoint(api.APIEndpoint),
		api.WithHTTPClient(&http.Client{Timeout: cfg.Telegram.RequestTimeout}),
		api.WithLogger(log.WithField("context", "bot_api")),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize bot API: %w", err)
	}
	if log.Level(cfg.LogLevel) == log.TraceLevel {
		botAPI.Debug = true
	}

	if err := announceBotCommands(ctx, botAPI); err != nil {
		log.WithError(err).Warn("failed to set bot commands")
	}

	dbClient, err := sqlite.NewSQLiteClient(ctx, cfg.DotPath, "bot.db")
	if err != nil {
		return nil, fmt.Errorf("initialize sqlite client: %w", err)
	}

	service := bot.NewService(ctx, botAPI, dbClient, cfg.DefaultLanguage, log.WithField("context", "service"))
	banService := moderationHandlers.NewBanService(botAPI, dbClient)
	spamControl := moderationHandlers.NewSpamControl(service, botAPI, dbClient, dbClient, cfg.SpamControl, banService, cfg.SpamControl.Verbose)

	gatekeeperHandler := chatHandlers.NewGatekeeper(service, botAPI, dbClient, dbClient, cfg, banService)
	adminHandler := adminHandlers.NewAdmin(service, botAPI, dbClient, dbClient, banService)

	llmAPI, err := configureLLM(cfg, log.WithField("context", "handlers"))
	if err != nil {
		_ = dbClient.Close()
		return nil, fmt.Errorf("configure llm: %w", err)
	}
	spamDetector := moderationHandlers.NewSpamDetector(llmAPI, log.WithField("context", "spam_detector"), cfg.LLM.RequestTimeout)

	reactorHandler := chatHandlers.NewReactor(service, botAPI, dbClient, dbClient, banService, spamControl, spamDetector, chatHandlers.Config{
		SpamControl: cfg.SpamControl,
	})

	updateHandlers := selectUpdateHandlers(cfg.EnabledHandlers, map[string]bot.Handler{
		handlerAdmin:      adminHandler,
		handlerGatekeeper: gatekeeperHandler,
		handlerReactor:    reactorHandler,
	})

	updateLoop := newUpdateLoopComponent(
		botAPI,
		configureUpdates(cfg.Telegram.PollTimeout),
		bot.NewPollingOptions(cfg.Telegram.RequestTimeout, cfg.Telegram.RecoveryWindow),
		bot.NewUpdateProcessor(service, updateHandlers...),
		errChan,
	)

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

func selectUpdateHandlers(enabled []string, available map[string]bot.Handler) []bot.Handler {
	handlers := make([]bot.Handler, 0, len(enabled))
	for _, name := range enabled {
		handler, ok := available[name]
		if !ok || handler == nil {
			log.WithField("handler", name).Warn("configured update handler is unavailable")
			continue
		}
		handlers = append(handlers, handler)
	}
	return handlers
}

func maskConfiguration(cfg *config.Config) *config.Config {
	maskedConfig := *cfg
	maskSecret := func(secret string) string {
		if secret == "" {
			return ""
		}
		return redactedConfigurationValue
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

func configureUpdates(pollTimeout time.Duration) api.UpdateConfig {
	updateConfig := api.NewUpdate(0)
	updateConfig.Timeout = durationSecondsCeil(pollTimeout)
	updateConfig.AllowedUpdates = []string{
		"message", "edited_message", "channel_post", "edited_channel_post",
		"message_reaction", "inline_query",
		"chosen_inline_result", "callback_query", "shipping_query",
		"pre_checkout_query", "poll", "poll_answer", "my_chat_member",
		"chat_member", "chat_join_request",
	}
	return updateConfig
}

func durationSecondsCeil(d time.Duration) int {
	seconds := int(d / time.Second)
	if d%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}

func formatPollingShutdown(err error) string {
	var pollingErr *bot.PollingRecoveryError
	if errors.As(err, &pollingErr) {
		return fmt.Sprintf("Polling recovery exhausted after %s: %v", pollingErr.SinceLastHealthy, pollingErr.Cause)
	}
	return fmt.Sprintf("Runtime error: %v", err)
}

func announceBotCommands(ctx context.Context, botAPI *api.BotAPI) error {
	if _, err := botAPI.RequestWithContext(ctx, api.NewDeleteMyCommands()); err != nil {
		return fmt.Errorf("delete commands: %w", err)
	}

	privateCommands := []api.BotCommand{
		{
			Command:     privateHelpCommand,
			Description: privateHelpCommandDescription,
		},
	}
	privateCommandsSet := api.NewSetMyCommandsWithScope(
		api.NewBotCommandScopeAllPrivateChats(),
		privateCommands...,
	)
	if _, err := botAPI.RequestWithContext(ctx, privateCommandsSet); err != nil {
		return fmt.Errorf("set private commands: %w", err)
	}

	groupCommands := []api.BotCommand{
		{
			Command:     voteBanCommand,
			Description: voteBanCommandDescription,
		},
	}
	groupCommandsSet := api.NewSetMyCommandsWithScope(
		api.NewBotCommandScopeAllGroupChats(),
		groupCommands...,
	)
	if _, err := botAPI.RequestWithContext(ctx, groupCommandsSet); err != nil {
		return fmt.Errorf("set group commands: %w", err)
	}

	groupAdminCommands := []api.BotCommand{
		{
			Command:     voteBanCommand,
			Description: voteBanCommandDescription,
		},
		{
			Command:     adminSettingsCommand,
			Description: adminSettingsCommandDescription,
		},
	}

	groupAdminCommandsSet := api.NewSetMyCommandsWithScope(
		api.NewBotCommandScopeAllChatAdministrators(),
		groupAdminCommands...,
	)
	if _, err := botAPI.RequestWithContext(ctx, groupAdminCommandsSet); err != nil {
		return fmt.Errorf("set group admin commands: %w", err)
	}

	return nil
}
