package config

import (
	"testing"
	"time"
)

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
