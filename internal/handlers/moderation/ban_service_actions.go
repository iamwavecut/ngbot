package handlers

import (
	"context"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

func (s *defaultBanService) MuteUser(ctx context.Context, chatID, userID int64) error {
	expiresAt := time.Now().Add(10 * time.Minute)
	config := api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     userID,
		},
		Permissions: &api.ChatPermissions{},
		UntilDate:   expiresAt.Unix(),

		UseIndependentChatPermissions: true,
	}

	if _, err := s.bot.Request(config); err != nil {
		return withPrivilegeError(err, "restrict")
	}

	restriction := &db.UserRestriction{
		UserID:       userID,
		ChatID:       chatID,
		RestrictedAt: time.Now(),
		ExpiresAt:    expiresAt,
		Reason:       "Spam suspect",
	}

	if err := s.db.AddRestriction(ctx, restriction); err != nil {
		return fmt.Errorf("failed to add restriction: %w", err)
	}

	return nil
}

func (s *defaultBanService) UnmuteUser(ctx context.Context, chatID, userID int64) error {
	config := api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     userID,
		},
		Permissions: &api.ChatPermissions{},
		UntilDate:   0,

		UseIndependentChatPermissions: false,
	}

	if _, err := s.bot.Request(config); err != nil {
		return withPrivilegeError(err, "unrestrict")
	}

	if err := s.db.RemoveRestriction(ctx, chatID, userID); err != nil {
		return fmt.Errorf("failed to remove restriction: %w", err)
	}

	return nil
}

func (s *defaultBanService) BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error {
	_ = messageID
	expiresAt := time.Now().Add(10 * time.Minute)
	config := api.BanChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate:      expiresAt.Unix(),
		RevokeMessages: true,
	}
	if _, err := s.bot.Request(config); err != nil {
		return withPrivilegeError(err, "ban")
	}

	restriction := &db.UserRestriction{
		UserID:       userID,
		ChatID:       chatID,
		RestrictedAt: time.Now(),
		ExpiresAt:    expiresAt,
		Reason:       "Spam detection",
	}

	if err := s.db.AddRestriction(ctx, restriction); err != nil {
		return fmt.Errorf("failed to add ban: %w", err)
	}

	return nil
}

func (s *defaultBanService) UnbanUser(ctx context.Context, chatID, userID int64) error {
	config := api.UnbanChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     userID,
		},
	}

	if _, err := s.bot.Request(config); err != nil {
		return fmt.Errorf("failed to unban user: %w", err)
	}

	if err := s.db.RemoveRestriction(ctx, chatID, userID); err != nil {
		return fmt.Errorf("failed to remove restriction: %w", err)
	}

	return nil
}

func (s *defaultBanService) IsRestricted(ctx context.Context, chatID, userID int64) (bool, error) {
	restriction, err := s.db.GetActiveRestriction(ctx, chatID, userID)
	if err != nil {
		return false, fmt.Errorf("failed to check restrictions: %w", err)
	}
	return restriction != nil && restriction.ExpiresAt.After(time.Now()), nil
}
