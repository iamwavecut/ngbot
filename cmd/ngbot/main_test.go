package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/gemini"
	"github.com/iamwavecut/ngbot/internal/adapters/llm/openai"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	log "github.com/sirupsen/logrus"
)

func TestVersionTextIdentifiesRevisionAndBuild(t *testing.T) {
	t.Parallel()

	text := versionText(buildIdentity{
		Version:   "v1.2.3",
		Revision:  "0123456789abcdef",
		BuildDate: "2026-08-13T16:00:00Z",
	})
	for _, want := range []string{"v1.2.3", "0123456789abcdef", "2026-08-13T16:00:00Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("version text %q is missing %q", text, want)
		}
	}
}

func TestRunHealthcheckRejectsUnhealthyResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	err := runHealthcheck(t.Context(), server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("runHealthcheck error = %v, want unhealthy status", err)
	}
}

func TestWebAppFatalErrorRequestsRestart(t *testing.T) {
	t.Parallel()

	shutdowns := make(chan shutdownSignal, 1)
	reportWebAppFatalError(shutdowns)(errors.New("listener failed"))
	shutdown := <-shutdowns
	if shutdown.exitCode != 1 || !strings.Contains(shutdown.message, "WebApp") {
		t.Fatalf("shutdown = %+v, want fatal WebApp restart", shutdown)
	}
}

func TestGatekeeperReconciliationCLIListsRedactedAndResolvesWithCAS(t *testing.T) {
	dataDir := t.TempDir()
	client, err := sqlite.NewSQLiteClient(t.Context(), dataDir, "bot.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	challenge := &db.Challenge{CommChatID: 1, UserID: 2, ChatID: -3, Status: db.ChallengeStatusApproveQueryPending, WebAppToken: "web-secret", JoinRequestQueryID: "query-secret", CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatal(err)
	}
	leased, claimed, err := client.ClaimChallengeAction(t.Context(), challenge.ChallengeID, "owner", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim: %#v %t %v", leased, claimed, err)
	}
	version, changed, err := client.BeginLeasedChallengeEffect(t.Context(), challenge.ChallengeID, "owner", leased.ActionVersion, challenge.Status, db.ChallengePhaseQueryAnswerStarted, now)
	if err != nil || !changed {
		t.Fatalf("begin: %d %t %v", version, changed, err)
	}
	errorSecret := "https://api.telegram.org/bot123456:SECRET/sendMessage body=query-secret web-secret"
	if reconciled, err := client.ReconcileLeasedChallengeVersion(t.Context(), challenge.ChallengeID, "owner", version, challenge.Status, 0, errorSecret, now); err != nil || !reconciled {
		t.Fatalf("reconcile: %t %v", reconciled, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{DotPath: dataDir}
	var output bytes.Buffer
	if err := runGatekeeperReconciliation(t.Context(), cfg, "list", &output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("query-secret")) || bytes.Contains(output.Bytes(), []byte("web-secret")) || bytes.Contains(output.Bytes(), []byte("SECRET")) || bytes.Contains(output.Bytes(), []byte("api.telegram.org")) {
		t.Fatalf("CLI leaked tokens: %s", output.String())
	}
	var record db.ChallengeReconciliation
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode list: %v output=%s", err, output.String())
	}
	command := fmt.Sprintf("resolve:%d:%d", record.ID, record.Version)
	if err := runGatekeeperReconciliation(t.Context(), cfg, command, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := runGatekeeperReconciliation(t.Context(), cfg, command, &bytes.Buffer{}); err == nil {
		t.Fatal("stale resolve CAS succeeded")
	}
}

func TestGatekeeperReconciliationCLIRequiresExclusiveProcessLock(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := sqlite.AcquireProcessLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := runGatekeeperReconciliation(t.Context(), &config.Config{DotPath: dataDir}, "list", &bytes.Buffer{}); err == nil {
		t.Fatal("reconciliation CLI opened database while service lock was held")
	}
}

type testUpdateHandler struct {
	name string
}

func (*testUpdateHandler) Handle(context.Context, *api.Update, *api.Chat, *api.User) (bool, error) {
	return true, nil
}

func TestConfigureUpdatesRequestsMessageReactionsOnly(t *testing.T) {
	t.Parallel()

	updates := configureUpdates(time.Minute).AllowedUpdates
	if !slices.Contains(updates, "message_reaction") {
		t.Fatalf("expected message_reaction updates, got %#v", updates)
	}
	if slices.Contains(updates, "message_reaction_count") {
		t.Fatalf("did not expect message_reaction_count updates, got %#v", updates)
	}
}

func TestMaskConfigurationRedactsCredentialsCompletely(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TelegramAPIToken: "telegram-prefix-secret-suffix",
		LLM: config.LLM{
			APIKey:       "llm-prefix-secret-suffix",
			GeminiAPIKey: "gemini-prefix-secret-suffix",
			OpenAIAPIKey: "openai-prefix-secret-suffix",
		},
	}

	masked := maskConfiguration(cfg)
	if masked.TelegramAPIToken != redactedConfigurationValue {
		t.Fatalf("telegram token = %q", masked.TelegramAPIToken)
	}
	if masked.LLM.APIKey != redactedConfigurationValue {
		t.Fatalf("llm key = %q", masked.LLM.APIKey)
	}
	if masked.LLM.GeminiAPIKey != redactedConfigurationValue {
		t.Fatalf("Gemini key = %q", masked.LLM.GeminiAPIKey)
	}
	if masked.LLM.OpenAIAPIKey != redactedConfigurationValue {
		t.Fatalf("OpenAI key = %q", masked.LLM.OpenAIAPIKey)
	}
}

func TestConfigureLLMUsesSelectedProviderCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		llm  config.LLM
		want any
	}{
		{
			name: "Gemini",
			llm:  config.LLM{Type: "gemini", GeminiAPIKey: "gemini-key"},
			want: (*gemini.API)(nil),
		},
		{
			name: "OpenAI",
			llm:  config.LLM{Type: "openai", OpenAIAPIKey: "openai-key", BaseURL: "https://api.openai.com/v1"},
			want: (*openai.API)(nil),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := configureLLM(&config.Config{EnabledHandlers: []string{handlerReactor}, LLM: tt.llm}, log.NewEntry(log.New()))
			if err != nil {
				t.Fatalf("configureLLM returned error: %v", err)
			}
			if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", tt.want) {
				t.Fatalf("adapter type = %T, want %T", got, tt.want)
			}
		})
	}
}

func TestConfigureLLMSkipsUnusedProvider(t *testing.T) {
	t.Parallel()

	got, err := configureLLM(&config.Config{
		EnabledHandlers: []string{handlerAdmin, handlerGatekeeper},
		LLM:             config.LLM{Type: "unsupported"},
	}, log.NewEntry(log.New()))
	if err != nil {
		t.Fatalf("unused LLM returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("unused LLM adapter = %T, want nil", got)
	}
}

func TestSelectUpdateHandlersPreservesConfiguredOrder(t *testing.T) {
	t.Parallel()

	admin := &testUpdateHandler{name: handlerAdmin}
	gatekeeper := &testUpdateHandler{name: handlerGatekeeper}
	reactor := &testUpdateHandler{name: handlerReactor}
	available := map[string]bot.Handler{
		handlerAdmin:      admin,
		handlerGatekeeper: gatekeeper,
		handlerReactor:    reactor,
		"disabled":        nil,
	}

	got := selectUpdateHandlers(
		[]string{handlerAdmin, "unknown", handlerGatekeeper, "disabled", handlerReactor},
		available,
	)
	want := []bot.Handler{admin, gatekeeper, reactor}
	if !slices.Equal(got, want) {
		t.Fatalf("selected handlers = %#v, want %#v", got, want)
	}
}

type commandRegistrationCall struct {
	method   string
	scope    string
	commands []api.BotCommand
}

func TestAnnounceBotCommandsRegistersPrivateHelp(t *testing.T) {
	var calls []commandRegistrationCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		switch method {
		case "getMe":
			writeTelegramResult(t, w, map[string]any{
				"id":         1,
				"is_bot":     true,
				"first_name": "Test",
				"username":   "testbot",
			})
			return
		case "deleteMyCommands":
			calls = append(calls, commandRegistrationCall{method: method})
			writeTelegramResult(t, w, true)
			return
		case "setMyCommands":
			form := parseRegistrationForm(t, r)
			calls = append(calls, commandRegistrationCall{
				method:   method,
				scope:    commandScopeType(t, form),
				commands: commandList(t, form),
			})
			writeTelegramResult(t, w, true)
			return
		default:
			t.Fatalf("unexpected telegram method: %s", method)
		}
	}))
	t.Cleanup(server.Close)

	botAPI, err := api.NewBotAPIWithOptions(
		"TEST_TOKEN",
		api.WithAPIEndpoint(fmt.Sprintf("%s/bot%%s/%%s", server.URL)),
		api.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("new bot api: %v", err)
	}

	if err := announceBotCommands(context.Background(), botAPI); err != nil {
		t.Fatalf("announce bot commands: %v", err)
	}

	if len(calls) != 4 {
		t.Fatalf("registration calls = %#v, want delete + 3 set calls", calls)
	}

	private := commandsForScope(t, calls, "all_private_chats")
	if !slices.Equal(private, []api.BotCommand{{Command: privateHelpCommand, Description: privateHelpCommandDescription}}) {
		t.Fatalf("private commands = %#v", private)
	}

	group := commandsForScope(t, calls, "all_group_chats")
	if !slices.Equal(group, []api.BotCommand{{Command: voteBanCommand, Description: voteBanCommandDescription}}) {
		t.Fatalf("group commands = %#v", group)
	}

	admin := commandsForScope(t, calls, "all_chat_administrators")
	wantAdmin := []api.BotCommand{
		{Command: voteBanCommand, Description: voteBanCommandDescription},
		{Command: adminSettingsCommand, Description: adminSettingsCommandDescription},
	}
	if !slices.Equal(admin, wantAdmin) {
		t.Fatalf("admin commands = %#v", admin)
	}
}

func TestNewTelegramBotAPIKeepsRawPayloadDebugDisabled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if method := path.Base(r.URL.Path); method != "getMe" {
			t.Fatalf("unexpected telegram method: %s", method)
		}
		writeTelegramResult(t, w, map[string]any{
			"id":         1,
			"is_bot":     true,
			"first_name": "Test",
			"username":   "testbot",
		})
	}))
	t.Cleanup(server.Close)

	botAPI, err := newTelegramBotAPI(
		"TEST_TOKEN",
		fmt.Sprintf("%s/bot%%s/%%s", server.URL),
		server.Client(),
	)
	if err != nil {
		t.Fatalf("new telegram bot api: %v", err)
	}
	if botAPI.Debug {
		t.Fatal("raw Telegram request and response logging must remain disabled")
	}
}

func writeTelegramResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func parseRegistrationForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return r.Form
}

func commandScopeType(t *testing.T, form url.Values) string {
	t.Helper()
	var scope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(form.Get("scope")), &scope); err != nil {
		t.Fatalf("unmarshal scope %q: %v", form.Get("scope"), err)
	}
	return scope.Type
}

func commandList(t *testing.T, form url.Values) []api.BotCommand {
	t.Helper()
	var commands []api.BotCommand
	if err := json.Unmarshal([]byte(form.Get("commands")), &commands); err != nil {
		t.Fatalf("unmarshal commands %q: %v", form.Get("commands"), err)
	}
	return commands
}

func commandsForScope(t *testing.T, calls []commandRegistrationCall, scope string) []api.BotCommand {
	t.Helper()
	for _, call := range calls {
		if call.scope == scope {
			return call.commands
		}
	}
	t.Fatalf("missing commands for scope %s in %#v", scope, calls)
	return nil
}
