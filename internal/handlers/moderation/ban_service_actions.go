package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
)

func (s *defaultBanService) MuteUser(ctx context.Context, chatID, userID int64) error {
	priorPermissions, err := s.effectiveMemberPermissions(ctx, chatID, userID)
	if err != nil {
		return fmt.Errorf("capture permissions before restriction: %w", err)
	}
	priorPermissionsJSON, err := json.Marshal(priorPermissions)
	if err != nil {
		return fmt.Errorf("encode permissions before restriction: %w", err)
	}
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

	if _, err := s.bot.RequestWithContext(ctx, config); err != nil {
		if isTelegramPrivilegeError(err) {
			s.MarkModerationUnavailable(chatID)
		}
		return withPrivilegeError(err, "restrict")
	}

	restriction := &db.UserRestriction{
		UserID:               userID,
		ChatID:               chatID,
		RestrictedAt:         time.Now(),
		ExpiresAt:            expiresAt,
		Reason:               "Spam suspect",
		PriorPermissionsJSON: string(priorPermissionsJSON),
	}

	if err := s.db.AddRestriction(ctx, restriction); err != nil {
		persistErr := fmt.Errorf("failed to add restriction: %w", err)
		if restoreErr := s.restorePermissions(ctx, chatID, userID, priorPermissions); restoreErr != nil {
			return errors.Join(persistErr, fmt.Errorf("restore permissions after persistence failure: %w", restoreErr))
		}
		return persistErr
	}

	return nil
}

func (s *defaultBanService) UnmuteUser(ctx context.Context, chatID, userID int64) error {
	restriction, err := s.db.GetActiveRestriction(ctx, chatID, userID)
	if err != nil {
		return fmt.Errorf("load restriction permissions: %w", err)
	}
	permissions, err := s.permissionsBeforeRestriction(ctx, chatID, restriction)
	if err != nil {
		return err
	}
	if err := s.restorePermissions(ctx, chatID, userID, permissions); err != nil {
		return err
	}

	if err := s.db.RemoveRestriction(ctx, chatID, userID); err != nil {
		return fmt.Errorf("failed to remove restriction: %w", err)
	}

	return nil
}

func (s *defaultBanService) restorePermissions(ctx context.Context, chatID, userID int64, permissions *api.ChatPermissions) error {
	config := api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     userID,
		},
		Permissions:                   permissions,
		UseIndependentChatPermissions: true,
	}

	if _, err := s.bot.RequestWithContext(ctx, config); err != nil {
		if isTelegramPrivilegeError(err) {
			s.MarkModerationUnavailable(chatID)
		}
		return withPrivilegeError(err, "unrestrict")
	}

	return nil
}

func (s *defaultBanService) BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error {
	return s.BanUserWithMessageUntil(ctx, chatID, userID, messageID, time.Now().Add(10*time.Minute))
}

func (s *defaultBanService) BanUserWithMessageUntil(ctx context.Context, chatID, userID int64, messageID int, expiresAt time.Time) error {
	_ = messageID
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
	if _, err := s.bot.RequestWithContext(ctx, config); err != nil {
		if isTelegramPrivilegeError(err) {
			s.MarkModerationUnavailable(chatID)
		}
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

	if _, err := s.bot.RequestWithContext(ctx, config); err != nil {
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

func (s *defaultBanService) effectiveMemberPermissions(ctx context.Context, chatID, userID int64) (*api.ChatPermissions, error) {
	member, err := bot.GetChatMember(ctx, s.bot, api.NewGetChatMember(chatID, userID))
	if err != nil {
		return nil, err
	}
	if member.Status == "restricted" {
		return chatMemberPermissions(member), nil
	}
	chat, err := bot.GetChat(ctx, s.bot, api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: chatID}})
	if err != nil {
		return nil, err
	}
	if chat.Permissions == nil {
		return &api.ChatPermissions{}, nil
	}
	permissions := *chat.Permissions
	return &permissions, nil
}

func (s *defaultBanService) permissionsBeforeRestriction(ctx context.Context, chatID int64, restriction *db.UserRestriction) (*api.ChatPermissions, error) {
	if restriction == nil {
		return nil, fmt.Errorf("prior permissions are unavailable")
	}
	if restriction.PriorPermissionsJSON == "" {
		chat, err := bot.GetChat(ctx, s.bot, api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: chatID}})
		if err != nil {
			return nil, fmt.Errorf("load effective chat permissions: %w", err)
		}
		if chat.Permissions == nil {
			return &api.ChatPermissions{}, nil
		}
		permissions := *chat.Permissions
		return &permissions, nil
	}
	permissions := &api.ChatPermissions{}
	if err := json.Unmarshal([]byte(restriction.PriorPermissionsJSON), permissions); err != nil {
		return nil, fmt.Errorf("decode prior permissions: %w", err)
	}
	return permissions, nil
}

func chatMemberPermissions(member api.ChatMember) *api.ChatPermissions {
	return &api.ChatPermissions{
		CanSendMessages:       member.CanSendMessages,
		CanSendAudios:         member.CanSendAudios,
		CanSendDocuments:      member.CanSendDocuments,
		CanSendPhotos:         member.CanSendPhotos,
		CanSendVideos:         member.CanSendVideos,
		CanSendVideoNotes:     member.CanSendVideoNotes,
		CanSendVoiceNotes:     member.CanSendVoiceNotes,
		CanSendPolls:          member.CanSendPolls,
		CanReactToMessages:    member.CanReactToMessages,
		CanSendOtherMessages:  member.CanSendOtherMessages,
		CanAddWebPagePreviews: member.CanAddWebPagePreviews,
		CanEditTag:            member.CanEditTag,
		CanChangeInfo:         member.CanChangeInfo,
		CanInviteUsers:        member.CanInviteUsers,
		CanPinMessages:        member.CanPinMessages,
		CanManageTopics:       member.CanManageTopics,
	}
}
