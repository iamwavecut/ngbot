package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

	banServiceHTTPTimeout = 10 * time.Second
	banServiceMaxRetries  = 3
	banServiceRetryStep   = 300 * time.Millisecond

	kvKeyLastDailyFetch  = "last_daily_fetch"
	kvKeyLastHourlyFetch = "last_hourly_fetch"
)

type BanService interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	CheckBan(ctx context.Context, userID int64) (bool, error)
	MuteUser(ctx context.Context, chatID, userID int64) error
	UnmuteUser(ctx context.Context, chatID, userID int64) error
	BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error
	UnbanUser(ctx context.Context, chatID, userID int64) error
	IsRestricted(ctx context.Context, chatID, userID int64) (bool, error)
	IsKnownBanned(userID int64) bool
}

type banStore interface {
	GetKV(ctx context.Context, key string) (string, error)
	SetKV(ctx context.Context, key string, value string) error
	UpsertBanlist(ctx context.Context, userIDs []int64) error
	GetBanlist(ctx context.Context) (map[int64]struct{}, error)
	AddRestriction(ctx context.Context, restriction *db.UserRestriction) error
	RemoveRestriction(ctx context.Context, chatID int64, userID int64) error
	GetActiveRestriction(ctx context.Context, chatID, userID int64) (*db.UserRestriction, error)
}

type defaultBanService struct {
	bot        *api.BotAPI
	db         banStore
	httpClient *http.Client

	knownBanned map[int64]struct{}
	mapMutex    sync.RWMutex

	runMutex  sync.Mutex
	started   bool
	runCancel context.CancelFunc
	workersWg sync.WaitGroup
}

var ErrNoPrivileges = fmt.Errorf("no privileges")

func NewBanService(bot *api.BotAPI, db banStore) BanService {
	return &defaultBanService{
		bot:         bot,
		db:          db,
		httpClient:  &http.Client{Timeout: banServiceHTTPTimeout},
		knownBanned: map[int64]struct{}{},
	}
}

func (s *defaultBanService) Start(ctx context.Context) error {
	s.runMutex.Lock()
	defer s.runMutex.Unlock()
	if s.started {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.runCancel = cancel

	s.workersWg.Add(1)
	go func() {
		defer s.workersWg.Done()
		if err := s.bootstrap(runCtx); err != nil && !errorsIsCanceled(err) {
			log.WithError(err).Error("Failed to bootstrap known banned users")
		}
	}()

	s.workersWg.Add(1)
	go func() {
		defer s.workersWg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := s.fetchHourlyIfNeeded(runCtx); err != nil && !errorsIsCanceled(err) {
					log.WithError(err).Error("Failed to fetch known banned users hourly")
				}
			}
		}
	}()

	s.started = true
	return nil
}

func (s *defaultBanService) Stop(ctx context.Context) error {
	s.runMutex.Lock()
	if !s.started {
		s.runMutex.Unlock()
		return nil
	}
	s.started = false
	cancel := s.runCancel
	s.runMutex.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.workersWg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *defaultBanService) bootstrap(ctx context.Context) error {
	lastDailyFetch, err := s.getLastDailyFetch(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get last daily fetch time")
	}

	lastHourlyFetch, err := s.getLastHourlyFetch(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to get last hourly fetch time")
	}

	if lastDailyFetch.IsZero() || time.Since(lastDailyFetch) >= 24*time.Hour {
		return s.FetchKnownBannedDaily(ctx)
	}
	if lastHourlyFetch.IsZero() || time.Since(lastHourlyFetch) >= time.Hour {
		return s.FetchKnownBanned(ctx)
	}
	return nil
}

func (s *defaultBanService) fetchHourlyIfNeeded(ctx context.Context) error {
	lastFetch, err := s.getLastHourlyFetch(ctx)
	if err != nil {
		return fmt.Errorf("get last hourly fetch: %w", err)
	}
	if !lastFetch.IsZero() && time.Since(lastFetch) < time.Hour {
		return nil
	}
	return s.FetchKnownBanned(ctx)
}

func (s *defaultBanService) IsKnownBanned(userID int64) bool {
	s.mapMutex.RLock()
	defer s.mapMutex.RUnlock()
	_, banned := s.knownBanned[userID]
	return banned
}

func (s *defaultBanService) CheckBan(ctx context.Context, userID int64) (bool, error) {
	if s.IsKnownBanned(userID) {
		return true, nil
	}

	url := fmt.Sprintf(accoutsAPIURLTemplate, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("accept", "text/plain")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

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
		s.markKnownBanned(userID)
	}
	return banInfo.Banned, nil
}

func (s *defaultBanService) setKnownBanned(banned map[int64]struct{}) {
	snapshot := make(map[int64]struct{}, len(banned))
	for userID := range banned {
		snapshot[userID] = struct{}{}
	}
	s.mapMutex.Lock()
	s.knownBanned = snapshot
	s.mapMutex.Unlock()
}

func (s *defaultBanService) markKnownBanned(userID int64) {
	s.mapMutex.Lock()
	s.knownBanned[userID] = struct{}{}
	s.mapMutex.Unlock()
}

func errorsIsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func withPrivilegeError(err error, operation string) error {
	if strings.Contains(err.Error(), MsgNoPrivileges) {
		return ErrNoPrivileges
	}
	return fmt.Errorf("failed to %s user: %w", operation, err)
}
