package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/gemini"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/openai"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
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
	databaseMaintenanceArgument     = "--database-maintenance"
	gatekeeperReconcileArgument     = "--gatekeeper-reconcile="
	healthcheckArgumentPrefix       = "--healthcheck="
	versionArgument                 = "--version"
)

var (
	version   = "dev"
	revision  = "unknown"
	buildDate = "unknown"
)

type buildIdentity struct {
	Version   string
	Revision  string
	BuildDate string
}

type updateLoopComponent struct {
	botAPI       *api.BotAPI
	updateConfig api.UpdateConfig
	polling      bot.PollingOptions
	dispatcher   *bot.DurableUpdateDispatcher
	errChan      chan<- shutdownSignal

	cancel context.CancelFunc
	done   chan struct{}
}

type shutdownSignal struct {
	message  string
	exitCode int
}

func newUpdateLoopComponent(botAPI *api.BotAPI, updateConfig api.UpdateConfig, polling bot.PollingOptions, updateStore bot.DurableUpdateStore, updateProcess *bot.UpdateProcessor, errChan chan<- shutdownSignal) *updateLoopComponent {
	dispatcher := bot.NewDurableUpdateDispatcher(
		updateStore,
		updateProcess.Process,
		updateProcess.Degrade,
		bot.DurableUpdateDispatcherOptions{
			MaxWorkers:    8,
			PendingBudget: botAPI.Buffer,
		},
		log.WithField("context", "update_dispatcher"),
	)
	polling.Persist = dispatcher.Persist
	return &updateLoopComponent{
		botAPI:       botAPI,
		updateConfig: updateConfig,
		polling:      polling,
		dispatcher:   dispatcher,
		errChan:      errChan,
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
	for _, argument := range os.Args[1:] {
		if argument == versionArgument {
			_, _ = fmt.Fprintln(os.Stdout, versionText(currentBuildIdentity()))
			return
		}
		if healthURL, ok := strings.CutPrefix(argument, healthcheckArgumentPrefix); ok {
			healthCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := runHealthcheck(healthCtx, &http.Client{Timeout: 3 * time.Second}, healthURL); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.WithField("error", err.Error()).Error("cant load config")
		os.Exit(1)
	}

	config.RegisterSecret(cfg.TelegramAPIToken)
	config.RegisterSecret(cfg.LLM.APIKey)
	config.RegisterSecret(cfg.LLM.GeminiAPIKey)
	config.RegisterSecret(cfg.LLM.OpenAIAPIKey)

	log.SetFormatter(&config.NbFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.Level(cfg.LogLevel))
	tool.SetLogger(log.StandardLogger())
	identity := currentBuildIdentity()
	log.WithFields(log.Fields{
		"build_date": identity.BuildDate,
		"revision":   identity.Revision,
		"version":    identity.Version,
	}).Info("Starting ngbot")
	for _, argument := range os.Args[1:] {
		if command, ok := strings.CutPrefix(argument, gatekeeperReconcileArgument); ok {
			reconcileCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := runGatekeeperReconciliation(reconcileCtx, &cfg, command, os.Stdout); err != nil {
				log.WithError(err).Error("Gatekeeper reconciliation failed")
				os.Exit(1)
			}
			return
		}
	}
	if slices.Contains(os.Args[1:], databaseMaintenanceArgument) {
		maintenanceCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runDatabaseMaintenance(maintenanceCtx, &cfg); err != nil {
			log.WithError(err).Error("Database maintenance failed")
			os.Exit(1)
		}
		log.Info("Database maintenance completed")
		return
	}
	processLock, err := sqlite.AcquireProcessLock(cfg.DotPath)
	if err != nil {
		log.WithField("error_code", db.SafeGatekeeperErrorCode(err)).Error("Failed to acquire database process lock")
		os.Exit(1)
	}
	defer func() { _ = processLock.Close() }()

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

func runGatekeeperReconciliation(ctx context.Context, cfg *config.Config, command string, output io.Writer) error {
	processLock, err := sqlite.AcquireProcessLock(cfg.DotPath)
	if err != nil {
		return err
	}
	defer func() { _ = processLock.Close() }()
	client, err := sqlite.NewSQLiteClient(ctx, cfg.DotPath, "bot.db")
	if err != nil {
		return fmt.Errorf("open reconciliation database: %w", err)
	}
	defer func() { _ = client.Close() }()
	now := time.Now()
	switch {
	case command == "list":
		records, err := client.GetChallengeReconciliations(ctx)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(output)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				return err
			}
		}
		return nil
	case command == "cleanup":
		count, err := client.CleanupResolvedChallengeReconciliations(ctx, now)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "cleaned=%d\n", count)
		return err
	case strings.HasPrefix(command, "resolve:"):
		id, version, err := parseReconciliationTarget(strings.TrimPrefix(command, "resolve:"))
		if err != nil {
			return err
		}
		resolved, err := client.ResolveChallengeReconciliation(ctx, id, version, "operator resolved", now)
		if err != nil {
			return err
		}
		if !resolved {
			return errors.New("reconciliation changed or is already resolved")
		}
		return nil
	default:
		return errors.New("expected list, cleanup, or resolve:<id>:<version>")
	}
}

func parseReconciliationTarget(value string) (int64, int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("reconciliation target must be <id>:<version>")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, errors.New("reconciliation id must be positive")
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version <= 0 {
		return 0, 0, errors.New("reconciliation version must be positive")
	}
	return id, version, nil
}

func runDatabaseMaintenance(ctx context.Context, cfg *config.Config) error {
	dbClient, err := sqlite.NewSQLiteClient(ctx, cfg.DotPath, "bot.db")
	if err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	if err := dbClient.Close(); err != nil {
		return fmt.Errorf("close migrated database: %w", err)
	}
	if err := sqlite.MaintainDatabase(ctx, cfg.DotPath, "bot.db"); err != nil {
		return fmt.Errorf("maintain database: %w", err)
	}
	return nil
}

func buildRuntime(ctx context.Context, cfg *config.Config, errChan chan<- shutdownSignal) (*lifecycle.Runtime, error) {
	llmAPI, err := configureLLM(cfg, log.WithField("context", "handlers"))
	if err != nil {
		return nil, fmt.Errorf("configure llm: %w", err)
	}

	botAPI, err := newTelegramBotAPI(
		cfg.TelegramAPIToken,
		api.APIEndpoint,
		&http.Client{Timeout: cfg.Telegram.RequestTimeout},
	)
	if err != nil {
		return nil, fmt.Errorf("initialize bot API: %w", err)
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
	spamControl := moderationHandlers.NewSpamControl(service, botAPI, dbClient, cfg.SpamControl, banService, cfg.SpamControl.Verbose)

	gatekeeperHandler := chatHandlers.NewGatekeeper(service, botAPI, dbClient, dbClient, cfg, banService)
	gatekeeperHandler.SetWebAppFatalErrorHandler(reportWebAppFatalError(errChan))
	adminHandler := adminHandlers.NewAdmin(service, botAPI, dbClient, dbClient, banService)
	banlistGuard := chatHandlers.NewBanlistGuard(botAPI, dbClient, banService)

	var spamDetector chatHandlers.SpamDetectorInterface
	if llmAPI != nil {
		spamDetector = moderationHandlers.NewSpamDetector(llmAPI, log.WithField("context", "spam_detector"), cfg.LLM.RequestTimeout)
	}

	reactorHandler := chatHandlers.NewReactor(service, botAPI, dbClient, dbClient, banService, spamControl, spamDetector, chatHandlers.Config{
		SpamControl: cfg.SpamControl,
	})

	availableHandlers := map[string]bot.Handler{
		handlerAdmin:      adminHandler,
		handlerGatekeeper: gatekeeperHandler,
		handlerReactor:    reactorHandler,
	}
	updateHandlers := mandatoryUpdateHandlers(cfg.EnabledHandlers, availableHandlers, banlistGuard, reactorHandler)

	updateLoop := newUpdateLoopComponent(
		botAPI,
		configureUpdates(cfg.Telegram.PollTimeout),
		bot.NewPollingOptions(cfg.Telegram.RequestTimeout, cfg.Telegram.RecoveryWindow),
		dbClient,
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

func currentBuildIdentity() buildIdentity {
	return buildIdentity{Version: version, Revision: revision, BuildDate: buildDate}
}

func versionText(identity buildIdentity) string {
	return fmt.Sprintf("ngbot version=%s revision=%s build_date=%s", identity.Version, identity.Revision, identity.BuildDate)
}

func runHealthcheck(ctx context.Context, client *http.Client, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("perform healthcheck: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned HTTP %d", response.StatusCode)
	}
	return nil
}

func reportWebAppFatalError(errChan chan<- shutdownSignal) func(error) {
	return func(err error) {
		select {
		case errChan <- shutdownSignal{message: fmt.Sprintf("WebApp server failed: %v", err), exitCode: 1}:
		default:
			log.WithError(err).Error("WebApp fatal error dropped")
		}
	}
}

func mandatoryUpdateHandlers(enabled []string, available map[string]bot.Handler, banlistGuard, moderationRouter bot.Handler) []bot.Handler {
	handlers := []bot.Handler{banlistGuard, moderationRouter}
	for _, handler := range selectUpdateHandlers(enabled, available) {
		if handler != moderationRouter {
			handlers = append(handlers, handler)
		}
	}
	return handlers
}

func newTelegramBotAPI(token, endpoint string, client *http.Client) (*api.BotAPI, error) {
	botAPI, err := api.NewBotAPIWithOptions(
		token,
		api.WithAPIEndpoint(endpoint),
		api.WithHTTPClient(client),
		api.WithLogger(log.WithField("context", "bot_api")),
	)
	if err != nil {
		return nil, err
	}
	botAPI.Debug = false
	return botAPI, nil
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
	maskedConfig.LLM.GeminiAPIKey = maskSecret(cfg.LLM.GeminiAPIKey)
	maskedConfig.LLM.OpenAIAPIKey = maskSecret(cfg.LLM.OpenAIAPIKey)
	return &maskedConfig
}

func configureLLM(cfg *config.Config, logger *log.Entry) (adapters.LLM, error) {
	if !slices.Contains(cfg.EnabledHandlers, handlerReactor) {
		return nil, nil
	}
	apiKey := cfg.LLM.APIKeyForProvider()
	switch cfg.LLM.Type {
	case config.LLMProviderOpenAI:
		return openai.NewOpenAI(
			apiKey,
			cfg.LLM.Model,
			cfg.LLM.BaseURL,
			logger.WithField("context", "llm"),
		)
	case config.LLMProviderGemini:
		return gemini.NewGemini(
			apiKey,
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
