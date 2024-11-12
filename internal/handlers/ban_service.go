package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

const (
	MsgNoPrivileges = "not enough rights to restrict/unrestrict chat member"
)

type BanService interface {
	CheckBan(ctx context.Context, userID int64) (bool, error)
	MuteUser(ctx context.Context, chatID, userID int64) error
	UnmuteUser(ctx context.Context, chatID, userID int64) error
	BanUser(ctx context.Context, chatID, userID int64, messageID int) error
	UnbanUser(ctx context.Context, chatID, userID int64) error
	IsRestricted(ctx context.Context, chatID, userID int64) (bool, error)
}

type defaultBanService struct {
	apiURL string
	bot    *api.BotAPI
	db     db.Client
}

var ErrNoPrivileges = fmt.Errorf("no privileges")

func NewBanService(apiURL string, bot *api.BotAPI, db db.Client) BanService {
	return &defaultBanService{
		apiURL: apiURL,
		bot:    bot,
		db:     db,
	}
}

type banInfo struct {
	OK         bool    `json:"ok"`
	UserID     int64   `json:"user_id"`
	Banned     bool    `json:"banned"`
	When       string  `json:"when"`
	Offenses   int     `json:"offenses"`
	SpamFactor float64 `json:"spam_factor"`
}

func (s *defaultBanService) CheckBan(ctx context.Context, userID int64) (bool, error) {
	url := fmt.Sprintf("%s?id=%d", s.apiURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var banInfo banInfo
	if err := json.NewDecoder(resp.Body).Decode(&banInfo); err != nil {
		return false, fmt.Errorf("failed to decode response: %w", err)
	}

	return banInfo.Banned, nil
}

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
		if strings.Contains(err.Error(), MsgNoPrivileges) {
			return ErrNoPrivileges
		}
		return fmt.Errorf("failed to restrict user: %w", err)
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
		if strings.Contains(err.Error(), MsgNoPrivileges) {
			return ErrNoPrivileges
		}
		return fmt.Errorf("failed to unrestrict user: %w", err)
	}

	if err := s.db.RemoveRestriction(ctx, chatID, userID); err != nil {
		return fmt.Errorf("failed to remove restriction: %w", err)
	}

	return nil
}

func (s *defaultBanService) BanUser(ctx context.Context, chatID, userID int64, messageID int) error {
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
		if strings.Contains(err.Error(), MsgNoPrivileges) {
			return ErrNoPrivileges
		}
		return fmt.Errorf("failed to restrict user: %w", err)
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
