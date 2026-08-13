package config

import (
	"testing"
	"time"
)

func TestLoadUsesProviderSpecificCredential(t *testing.T) {
	t.Setenv("NG_TOKEN", "telegram-token")
	t.Setenv("NG_HANDLERS", "reactor")
	t.Setenv("NG_LLM_API_TYPE", "gemini")
	t.Setenv("NG_LLM_GEMINI_API_KEY", "gemini-specific")
	t.Setenv("NG_LLM_OPENAI_API_KEY", "openai-unused")
	t.Setenv("NG_LLM_API_KEY", "legacy-unused")
	t.Setenv("NG_DOT_PATH", t.TempDir())
	t.Setenv("NG_TELEGRAM_POLL_TIMEOUT", "60s")
	t.Setenv("NG_TELEGRAM_REQUEST_TIMEOUT", "75s")
	t.Setenv("NG_TELEGRAM_RECOVERY_WINDOW", "10m")
	t.Setenv("NG_SPAM_MESSAGE_PROBATION_DURATION", "3h")
	t.Setenv("NG_GATEKEEPER_WEBAPP_PUBLIC_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.LLM.APIKeyForProvider(); got != "gemini-specific" {
		t.Fatalf("selected credential = %q", got)
	}
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid telegram timings",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
			},
		},
		{
			name: "request timeout must exceed poll timeout",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 60 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
			},
			wantErr: true,
		},
		{
			name: "recovery window must exceed request timeout",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 75 * time.Second,
				},
			},
			wantErr: true,
		},
		{
			name: "valid gatekeeper web app public url",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
				GatekeeperWebApp: GatekeeperWebApp{
					PublicURL: "https://guard.example",
				},
			},
		},
		{
			name: "gatekeeper web app public url must be absolute",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
				GatekeeperWebApp: GatekeeperWebApp{
					PublicURL: "/gatekeeper",
				},
			},
			wantErr: true,
		},
		{
			name: "gatekeeper web app public url must be origin only",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
				GatekeeperWebApp: GatekeeperWebApp{PublicURL: "https://guard.example/prefix?source=x#fragment"},
			},
			wantErr: true,
		},
		{
			name: "gatekeeper web app public url rejects user info",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
				GatekeeperWebApp: GatekeeperWebApp{PublicURL: "https://user:password@guard.example"},
			},
			wantErr: true,
		},
		{
			name: "public http web app url is rejected",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
				GatekeeperWebApp: GatekeeperWebApp{PublicURL: "http://guard.example"},
			},
			wantErr: true,
		},
		{
			name: "loopback http web app url is accepted",
			cfg: Config{
				LLM:         LLM{RequestTimeout: 45 * time.Second},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
				GatekeeperWebApp: GatekeeperWebApp{PublicURL: "http://127.0.0.1:8080"},
			},
		},
		{
			name: "llm request timeout must be positive",
			cfg: Config{
				EnabledHandlers: []string{"reactor"},
				LLM: LLM{
					APIKey: "legacy-key",
					Type:   "gemini",
				},
				SpamControl: SpamControl{MessageProbationDuration: 3 * time.Hour},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
			},
			wantErr: true,
		},
		{
			name: "message probation duration must be positive",
			cfg: Config{
				LLM: LLM{RequestTimeout: 45 * time.Second},
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 10 * time.Minute,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateConfig(&tt.cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidateConfigNormalizesProviderConfiguration(t *testing.T) {
	t.Parallel()

	cfg := validConfigForLLM()
	cfg.LLM = LLM{
		GeminiAPIKey:   "  gemini-key  ",
		Model:          "  gemini-2.5-flash-lite  ",
		BaseURL:        "  https://api.openai.com/v1/  ",
		Type:           "  GeMiNi  ",
		RequestTimeout: 45 * time.Second,
	}
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("validateConfig returned error: %v", err)
	}
	if cfg.LLM.Type != "gemini" {
		t.Fatalf("provider = %q, want gemini", cfg.LLM.Type)
	}
	if cfg.LLM.Model != "gemini-2.5-flash-lite" {
		t.Fatalf("model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.GeminiAPIKey != "gemini-key" {
		t.Fatalf("gemini key was not normalized")
	}
	if cfg.LLM.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("base URL = %q", cfg.LLM.BaseURL)
	}
}

func TestValidateConfigRequiresOnlySelectedProviderCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		llm     LLM
		wantErr bool
	}{
		{
			name: "Gemini provider-specific credential",
			llm:  LLM{Type: "gemini", GeminiAPIKey: "gemini-key", RequestTimeout: 45 * time.Second},
		},
		{
			name: "OpenAI provider-specific credential",
			llm:  LLM{Type: "openai", OpenAIAPIKey: "openai-key", BaseURL: "https://api.openai.com/v1", RequestTimeout: 45 * time.Second},
		},
		{
			name: "legacy Gemini credential fallback",
			llm:  LLM{Type: "gemini", APIKey: "legacy-key", RequestTimeout: 45 * time.Second},
		},
		{
			name:    "selected credential missing",
			llm:     LLM{Type: "gemini", OpenAIAPIKey: "wrong-provider-key", RequestTimeout: 45 * time.Second},
			wantErr: true,
		},
		{
			name:    "unsupported provider is not inferred from available key",
			llm:     LLM{Type: "other", GeminiAPIKey: "gemini-key", OpenAIAPIKey: "openai-key", RequestTimeout: 45 * time.Second},
			wantErr: true,
		},
		{
			name:    "OpenAI endpoint must use HTTPS",
			llm:     LLM{Type: "openai", OpenAIAPIKey: "openai-key", BaseURL: "http://api.openai.com/v1", RequestTimeout: 45 * time.Second},
			wantErr: true,
		},
		{
			name:    "model must be one identifier",
			llm:     LLM{Type: "gemini", GeminiAPIKey: "gemini-key", Model: "two models", RequestTimeout: 45 * time.Second},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfigForLLM()
			cfg.LLM = tt.llm
			err := validateConfig(&cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidateConfigSkipsLLMWhenReactorIsDisabled(t *testing.T) {
	t.Parallel()

	cfg := validConfigForLLM()
	cfg.EnabledHandlers = []string{"admin", "gatekeeper"}
	cfg.LLM = LLM{}
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("unused LLM configuration blocked startup: %v", err)
	}
}

func validConfigForLLM() Config {
	return Config{
		EnabledHandlers: []string{"reactor"},
		SpamControl:     SpamControl{MessageProbationDuration: 3 * time.Hour},
		Telegram: Telegram{
			PollTimeout:    60 * time.Second,
			RequestTimeout: 75 * time.Second,
			RecoveryWindow: 10 * time.Minute,
		},
	}
}
