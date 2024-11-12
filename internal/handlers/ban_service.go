package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/pkg/errors"
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
		return false, errors.Wrap(err, "failed to create request")
	}
	req.Header.Set("accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, errors.Wrap(err, "failed to send request")
	}
	defer resp.Body.Close()

	var banInfo banInfo
	if err := json.NewDecoder(resp.Body).Decode(&banInfo); err != nil {
		return false, errors.Wrap(err, "failed to decode response")
	}

	return banInfo.Banned, nil
}

func (s *defaultBanService) MuteUser(ctx context.Context, chatID, userID int64) error {
	restriction := &db.UserRestriction{
		UserID:       userID,
		ChatID:       chatID,
		RestrictedAt: time.Now(),
		ExpiresAt:    time.Now().Add(10 * time.Minute), // Default 10m ban
		Reason:       "Spam suspect",
	}

	if err := s.db.AddRestriction(ctx, restriction); err != nil {
		return errors.Wrap(err, "failed to add restriction")
	}

	config := api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     userID,
		},
		Permissions: &api.ChatPermissions{},
		UntilDate:   restriction.ExpiresAt.Unix(),

		UseIndependentChatPermissions: true,
	}

	if _, err := s.bot.Request(config); err != nil {
		return errors.Wrap(err, "failed to restrict user")
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
		return errors.Wrap(err, "failed to unrestrict user")
	}

	if err := s.db.RemoveRestriction(ctx, chatID, userID); err != nil {
		return errors.Wrap(err, "failed to remove restriction")
	}

	return nil
}

func (s *defaultBanService) BanUser(ctx context.Context, chatID, userID int64, messageID int) error {
	restriction := &db.UserRestriction{
		UserID:       userID,
		ChatID:       chatID,
		RestrictedAt: time.Now(),
		ExpiresAt:    time.Now().Add(10 * time.Minute), // Default 10m ban
		Reason:       "Spam detection",
	}

	if err := s.db.AddRestriction(ctx, restriction); err != nil {
		return errors.Wrap(err, "failed to add ban")
	}

	config := api.BanChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate:      restriction.ExpiresAt.Unix(),
		RevokeMessages: true,
	}

	if _, err := s.bot.Request(config); err != nil {
		return errors.Wrap(err, "failed to restrict user")
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
		return errors.Wrap(err, "failed to unban user")
	}

	if err := s.db.RemoveRestriction(ctx, chatID, userID); err != nil {
		return errors.Wrap(err, "failed to remove restriction")
	}

	return nil
}

func (s *defaultBanService) IsRestricted(ctx context.Context, chatID, userID int64) (bool, error) {
	restriction, err := s.db.GetActiveRestriction(ctx, chatID, userID)
	if err != nil {
		return false, errors.Wrap(err, "failed to check restrictions")
	}
	return restriction != nil && restriction.ExpiresAt.After(time.Now()), nil
}
