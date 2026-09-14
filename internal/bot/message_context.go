package bot

import (
	"context"
	"fmt"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
)

type messageContextDeleter interface {
	DeleteMessageContext(ctx context.Context, chatID int64, messageID int) error
}

func DeleteChatMessageAndContext(ctx context.Context, botAPI *api.BotAPI, store messageContextDeleter, chatID int64, messageID int) error {
	if err := DeleteChatMessage(ctx, botAPI, chatID, messageID); err != nil {
		message := strings.ToUpper(err.Error())
		if !strings.Contains(message, "MESSAGE TO DELETE NOT FOUND") && !strings.Contains(message, "MESSAGE_ID_INVALID") {
			return err
		}
	}
	if err := store.DeleteMessageContext(ctx, chatID, messageID); err != nil {
		return fmt.Errorf("delete saved message context: %w", err)
	}
	return nil
}
