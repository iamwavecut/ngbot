package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const (
	MsgNoPrivileges = "not enough rights to restrict/unrestrict chat member"

	banlistURL       = "https://lols.bot/spam/banlist.txt"
	banlistURLHourly = "https://lols.bot/spam/banlist-1h.txt"
	scammersURL      = "https://lols.bot/scammers.txt"

	accoutsAPIURLTemplate = "https://api.lols.bot/account?id=%v"

	// KV store keys
	kvKeyLastDailyFetch  = "last_daily_fetch"
	kvKeyLastHourlyFetch = "last_hourly_fetch"
)

type BanService interface {
	CheckBan(ctx context.Context, userID int64) (bool, error)
	MuteUser(ctx context.Context, chatID, userID int64) error
	UnmuteUser(ctx context.Context, chatID, userID int64) error
	BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error
	UnbanUser(ctx context.Context, chatID, userID int64) error
	IsRestricted(ctx context.Context, chatID, userID int64) (bool, error)
	IsKnownBanned(userID int64) bool
}

type defaultBanService struct {
	bot         *api.BotAPI
	db          db.Client
	knownBanned map[int64]struct{}
	shutdown    chan struct{}
}

var ErrNoPrivileges = fmt.Errorf("no privileges")

func NewBanService(bot *api.BotAPI, db db.Client) BanService {
	s := &defaultBanService{
		bot:         bot,
		db:          db,
		knownBanned: map[int64]struct{}{},
		shutdown:    make(chan struct{}),
	}

	ctx := context.Background()
	lastDailyFetch, err := s.getLastDailyFetch(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get last daily fetch time")
	}

	lastHourlyFetch, err := s.getLastHourlyFetch(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get last hourly fetch time")
	}

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if lastDailyFetch.IsZero() || time.Since(lastDailyFetch) > time.Hour {
			if err := s.FetchKnownBannedDaily(ctx); err != nil {
				log.WithError(err).Error("Failed to fetch known banned users daily")
			}
		} else if lastHourlyFetch.IsZero() || time.Since(lastHourlyFetch) <= time.Hour {
			if err := s.FetchKnownBanned(ctx); err != nil {
				log.WithError(err).Error("Failed to fetch known banned users hourly")
			}
		}
	}()

	go func() {
		for {
			select {
			case <-s.shutdown:
				log.WithField("routine", "FetchKnownBanned").Info("Graceful shutdown completed")
				return
			case <-time.After(time.Hour):
				ctx := context.Background()
				lastFetch, err := s.getLastHourlyFetch(ctx)
				if err != nil {
					log.WithError(err).Error("Failed to get last hourly fetch time")
					continue
				}

				// Only fetch if last fetch was more than 1 hour ago
				if lastFetch.IsZero() || time.Since(lastFetch) >= time.Hour {
					if err := s.FetchKnownBanned(ctx); err != nil {
						log.WithError(err).Error("Failed to fetch known banned users hourly")
					}
				}
			}
		}
	}()
	wg.Wait()
	return s
}

func fetchURLs(ctx context.Context, urls []string) (map[int64]struct{}, error) {
	results := make(map[int64]struct{})
	for _, url := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("accept", "text/plain")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			userIDStr := scanner.Text()
			if userIDStr == "" {
				continue
			}
			userID, err := strconv.ParseInt(userIDStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse user ID: %w", err)
			}
			results[userID] = struct{}{}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("failed to scan response body: %w", err)
		}
	}
	return results, nil
}

func (s *defaultBanService) getLastDailyFetch(ctx context.Context) (time.Time, error) {
	val, err := s.db.GetKV(ctx, kvKeyLastDailyFetch)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last daily fetch time: %w", err)
	}
	if val == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse last daily fetch time: %w", err)
	}
	return t, nil
}

func (s *defaultBanService) getLastHourlyFetch(ctx context.Context) (time.Time, error) {
	val, err := s.db.GetKV(ctx, kvKeyLastHourlyFetch)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last hourly fetch time: %w", err)
	}
	if val == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse last hourly fetch time: %w", err)
	}
	return t, nil
}

func (s *defaultBanService) updateLastDailyFetch(ctx context.Context) error {
	return s.db.SetKV(ctx, kvKeyLastDailyFetch, time.Now().Format(time.RFC3339))
}

func (s *defaultBanService) updateLastHourlyFetch(ctx context.Context) error {
	return s.db.SetKV(ctx, kvKeyLastHourlyFetch, time.Now().Format(time.RFC3339))
}

func (s *defaultBanService) FetchKnownBannedDaily(ctx context.Context) error {
	results, err := fetchURLs(ctx, []string{scammersURL, banlistURL})
	if err != nil {
		return fmt.Errorf("failed to fetch known banned daily: %w", err)
	}

	userIDs := make([]int64, 0, len(results))
	for userID := range results {
		userIDs = append(userIDs, userID)
	}
	if err := s.db.UpsertBanlist(ctx, userIDs); err != nil {
		return fmt.Errorf("failed to upsert banlist: %w", err)
	}
	fullBanlist, err := s.db.GetBanlist(ctx)
	if err != nil {
		return fmt.Errorf("failed to get banlist: %w", err)
	}
	s.knownBanned = fullBanlist
	log.WithField("count", len(fullBanlist)).Debug("fetched known banned ids daily")

	if err := s.updateLastDailyFetch(ctx); err != nil {
		log.WithError(err).Error("Failed to update last daily fetch time")
	}

	return nil
}

func (s *defaultBanService) FetchKnownBanned(ctx context.Context) error {
	results, err := fetchURLs(ctx, []string{banlistURLHourly})
	if err != nil {
		return fmt.Errorf("failed to fetch known banned hourly: %w", err)
	}
	userIDs := make([]int64, 0, len(results))
	for userID := range results {
		userIDs = append(userIDs, userID)
	}
	if err := s.db.UpsertBanlist(ctx, userIDs); err != nil {
		return fmt.Errorf("failed to upsert banlist: %w", err)
	}
	fullBanlist, err := s.db.GetBanlist(ctx)
	if err != nil {
		return fmt.Errorf("failed to get banlist: %w", err)
	}
	s.knownBanned = fullBanlist
	log.WithField("count", len(fullBanlist)).Debug("fetched known banned ids hourly")

	if err := s.updateLastHourlyFetch(ctx); err != nil {
		log.WithError(err).Error("Failed to update last hourly fetch time")
	}

	return nil
}

func (s *defaultBanService) IsKnownBanned(userID int64) bool {
	_, banned := s.knownBanned[userID]
	return banned
}

func (s *defaultBanService) CheckBan(ctx context.Context, userID int64) (bool, error) {
	banned := s.IsKnownBanned(userID)
	if banned {
		return true, nil
	}

	url := fmt.Sprintf(accoutsAPIURLTemplate, userID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("accept", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var banInfo struct {
		OK         bool    `json:"ok"`
		UserID     int64   `json:"user_id"`
		Banned     bool    `json:"banned"`
		When       string  `json:"when"`
		Offenses   int     `json:"offenses"`
		SpamFactor float64 `json:"spam_factor"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&banInfo); err != nil {
		return false, fmt.Errorf("failed to decode response: %w", err)
	}
	if banInfo.Banned {
		s.knownBanned[userID] = struct{}{}
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

func (s *defaultBanService) BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error {
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
		return fmt.Errorf("failed to ban user: %w", err)
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
