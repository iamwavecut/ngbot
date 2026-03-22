package handlers

import (
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestCommandTargetsCurrentBot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		messageText string
		entityLen   int
		botUserName string
		want        bool
	}{
		{
			name:        "unnamed command is accepted",
			messageText: "/ban",
			entityLen:   len("/ban"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named current bot command is accepted",
			messageText: "/ban@ngbot",
			entityLen:   len("/ban@ngbot"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named current bot command is case insensitive",
			messageText: "/ban@NgBoT",
			entityLen:   len("/ban@NgBoT"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named foreign bot command is ignored",
			messageText: "/ban@otherbot",
			entityLen:   len("/ban@otherbot"),
			botUserName: "ngbot",
			want:        false,
		},
		{
			name:        "named command is ignored when bot username is empty",
			messageText: "/ban@ngbot",
			entityLen:   len("/ban@ngbot"),
			botUserName: "",
			want:        false,
		},
		{
			name:        "non command is ignored",
			messageText: "ban",
			entityLen:   0,
			botUserName: "ngbot",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := &api.Message{Text: tt.messageText}
			if tt.entityLen > 0 {
				msg.Entities = []api.MessageEntity{{
					Type:   "bot_command",
					Offset: 0,
					Length: tt.entityLen,
				}}
			}

			got := commandTargetsCurrentBot(msg, tt.botUserName)
			if got != tt.want {
				t.Fatalf("commandTargetsCurrentBot() = %v, want %v", got, tt.want)
			}
		})
	}
}
