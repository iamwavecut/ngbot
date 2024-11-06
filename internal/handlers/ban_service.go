package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/OvyFlash/telegram-bot-api/v6"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/pkg/errors"
)

type defaultBanService struct {
	apiURL string
	bot    *api.BotAPI
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

func (s *defaultBanService) BanUser(ctx context.Context, chatID, userID int64, messageID int) error {
	if err := bot.DeleteChatMessage(s.bot, chatID, messageID); err != nil {
		return errors.Wrap(err, "failed to delete message")
	}
	if err := bot.BanUserFromChat(s.bot, userID, chatID); err != nil {
		return errors.Wrap(err, "failed to ban user")
	}
	return nil
}
