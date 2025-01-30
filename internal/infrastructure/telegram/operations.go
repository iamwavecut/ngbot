package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
)

// Operations provides common Telegram bot operations
type Operations struct {
	bot *api.BotAPI
}

// NewOperations creates a new Operations instance
func NewOperations(bot *api.BotAPI) *Operations {
	return &Operations{bot: bot}
}

// DeleteMessage deletes a message from a chat
func (o *Operations) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	_, err := o.bot.Request(api.NewDeleteMessage(chatID, messageID))
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}
	return nil
}

// BanUser bans a user from a chat
func (o *Operations) BanUser(ctx context.Context, userID int64, chatID int64) error {
	config := api.BanChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate:      time.Now().Add(10 * time.Minute).Unix(),
		RevokeMessages: true,
	}
	_, err := o.bot.Request(config)
	if err != nil {
		if strings.Contains(err.Error(), "not enough rights") {
			return fmt.Errorf("not enough rights to ban user")
		}
		return fmt.Errorf("failed to ban user: %w", err)
	}
	return nil
}

// RestrictUser restricts a user's ability to chat
func (o *Operations) RestrictUser(ctx context.Context, userID int64, chatID int64) error {
	untilDate := time.Now().Add(24 * time.Hour)
	config := api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate: untilDate.Unix(),
		Permissions: &api.ChatPermissions{
			CanSendMessages:       false,
			CanSendOtherMessages:  false,
			CanAddWebPagePreviews: false,
		},
	}
	_, err := o.bot.Request(config)
	if err != nil {
		return fmt.Errorf("failed to restrict user: %w", err)
	}
	return nil
}

// UnrestrictUser removes chat restrictions from a user
func (o *Operations) UnrestrictUser(ctx context.Context, userID int64, chatID int64) error {
	config := api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		Permissions: &api.ChatPermissions{
			CanSendMessages:       true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
		},
	}
	_, err := o.bot.Request(config)
	if err != nil {
		return fmt.Errorf("failed to unrestrict user: %w", err)
	}
	return nil
}

// ApproveJoinRequest approves a chat join request
func (o *Operations) ApproveJoinRequest(ctx context.Context, userID int64, chatID int64) error {
	config := api.ApproveChatJoinRequestConfig{
		ChatConfig: api.ChatConfig{
			ChatID: chatID,
		},
		UserID: userID,
	}
	_, err := o.bot.Request(config)
	if err != nil {
		return fmt.Errorf("failed to approve join request: %w", err)
	}
	return nil
}

// DeclineJoinRequest declines a chat join request
func (o *Operations) DeclineJoinRequest(ctx context.Context, userID int64, chatID int64) error {
	config := api.DeclineChatJoinRequest{
		ChatConfig: api.ChatConfig{
			ChatID: chatID,
		},
		UserID: userID,
	}
	_, err := o.bot.Request(config)
	if err != nil {
		return fmt.Errorf("failed to decline join request: %w", err)
	}
	return nil
}
