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
				Telegram: Telegram{
					PollTimeout:    60 * time.Second,
					RequestTimeout: 75 * time.Second,
					RecoveryWindow: 75 * time.Second,
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
