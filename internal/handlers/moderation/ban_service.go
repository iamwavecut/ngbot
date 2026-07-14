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
	casExportURL     = "https://api.cas.chat/export.csv"
	casCheckURL      = "https://api.cas.chat/check?user_id=%d"

	accoutsAPIURLTemplate = "https://api.lols.bot/account?id=%v"

	banServiceHTTPTimeout = 10 * time.Second
	banServiceMaxRetries  = 3
	banServiceRetryStep   = 300 * time.Millisecond
	banlistHourlyTTL      = 26 * time.Hour
	banlistOnlineTTL      = 24 * time.Hour

	banlistFeedDaily  = "daily"
	banlistFeedHourly = "hourly"
	banlistFeedOnline = "online"

	kvKeyLastDailyFetch  = "last_daily_fetch"
	kvKeyLastHourlyFetch = "last_hourly_fetch"
	logFieldProvider     = "provider"
	logFieldUserID       = "user_id"
	logFieldError        = "error"
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
	ApplyBanlistSource(ctx context.Context, provider, feedType, generation string, userIDs []int64, seenAt time.Time, expiresAt *time.Time, replace bool) error
	GetBanlist(ctx context.Context) (map[int64]struct{}, error)
	AddRestriction(ctx context.Context, restriction *db.UserRestriction) error
	RemoveRestriction(ctx context.Context, chatID int64, userID int64) error
	GetActiveRestriction(ctx context.Context, chatID, userID int64) (*db.UserRestriction, error)
	RemoveExpiredRestrictions(ctx context.Context) error
}

type banlistProvider struct {
	name       string
	dailyURLs  []string
	hourlyURLs []string
	check      func(ctx context.Context, client *http.Client, userID int64) (bool, error)
}

type defaultBanService struct {
	bot        *api.BotAPI
	db         banStore
	httpClient *http.Client
	providers  []banlistProvider

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
		providers:   defaultBanlistProviders(),
		knownBanned: map[int64]struct{}{},
	}
}

func defaultBanlistProviders() []banlistProvider {
	return []banlistProvider{
		{
			name:       "lols",
			dailyURLs:  []string{scammersURL, banlistURL},
			hourlyURLs: []string{banlistURLHourly},
			check: func(ctx context.Context, client *http.Client, userID int64) (bool, error) {
				return checkLoLsBan(ctx, client, accoutsAPIURLTemplate, userID)
			},
		},
		{
			name:      "cas",
			dailyURLs: []string{casExportURL},
			check: func(ctx context.Context, client *http.Client, userID int64) (bool, error) {
				return checkCASBan(ctx, client, casCheckURL, userID)
			},
		},
	}
}

func (s *defaultBanService) Start(ctx context.Context) error {
	s.runMutex.Lock()
	defer s.runMutex.Unlock()
	if s.started {
		return nil
	}
	if err := s.loadKnownBannedFromDB(ctx); err != nil {
		return fmt.Errorf("load known banned from db: %w", err)
	}
	if err := s.cleanupExpiredRestrictions(ctx); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.runCancel = cancel

	s.workersWg.Go(func() {
		if err := s.refreshKnownBanned(runCtx); err != nil && !errorsIsCanceled(err) {
			log.WithError(err).Error("Failed to bootstrap known banned users")
		}
	})

	s.workersWg.Go(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := s.cleanupExpiredRestrictions(runCtx); err != nil && !errorsIsCanceled(err) {
					log.WithError(err).Error("Failed to clean up expired restrictions")
				}
				if err := s.refreshKnownBanned(runCtx); err != nil && !errorsIsCanceled(err) {
					log.WithError(err).Error("Failed to refresh known banned users")
				}
			}
		}
	})

	s.started = true
	return nil
}

func (s *defaultBanService) cleanupExpiredRestrictions(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	if err := s.db.RemoveExpiredRestrictions(ctx); err != nil {
		return fmt.Errorf("remove expired restrictions: %w", err)
	}
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
	if err := s.loadKnownBannedFromDB(ctx); err != nil {
		return fmt.Errorf("load known banned from db: %w", err)
	}
	return s.refreshKnownBanned(ctx)
}

func (s *defaultBanService) refreshKnownBanned(ctx context.Context) error {
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

func (s *defaultBanService) IsKnownBanned(userID int64) bool {
	s.mapMutex.RLock()
	defer s.mapMutex.RUnlock()
	_, banned := s.knownBanned[userID]
	return banned
}

func (s *defaultBanService) CheckBan(ctx context.Context, userID int64) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	if s.IsKnownBanned(userID) {
		return true, nil
	}

	providers := s.banlistProviders()
	checkers := make([]banlistProvider, 0, len(providers))
	for _, provider := range providers {
		if provider.check != nil {
			checkers = append(checkers, provider)
		}
	}
	if len(checkers) == 0 {
		return false, nil
	}

	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type checkResult struct {
		provider string
		banned   bool
		err      error
	}

	results := make(chan checkResult, len(checkers))
	for _, provider := range checkers {
		provider := provider
		go func() {
			banned, err := provider.check(checkCtx, s.httpClient, userID)
			results <- checkResult{
				provider: provider.name,
				banned:   banned,
				err:      err,
			}
		}()
	}

	for range checkers {
		result := <-results
		if result.err != nil {
			if errorsIsCanceled(result.err) && ctx.Err() != nil {
				return false, ctx.Err()
			}
			log.WithFields(log.Fields{
				logFieldProvider: result.provider,
				logFieldUserID:   userID,
				logFieldError:    result.err.Error(),
			}).Warn("external ban check failed open")
			continue
		}
		if result.banned {
			cancel()
			s.rememberKnownBanned(ctx, userID, result.provider)
			return true, nil
		}
	}

	return false, nil
}

func checkLoLsBan(ctx context.Context, client *http.Client, urlTemplate string, userID int64) (bool, error) {
	url := fmt.Sprintf(urlTemplate, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
	return banInfo.Banned, nil
}

func checkCASBan(ctx context.Context, client *http.Client, urlTemplate string, userID int64) (bool, error) {
	url := fmt.Sprintf(urlTemplate, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	var banInfo struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&banInfo); err != nil {
		return false, fmt.Errorf("failed to decode response: %w", err)
	}
	return banInfo.OK, nil
}

func (s *defaultBanService) banlistProviders() []banlistProvider {
	if len(s.providers) > 0 {
		return s.providers
	}
	return defaultBanlistProviders()
}

func (s *defaultBanService) loadKnownBannedFromDB(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	fullBanlist, err := s.db.GetBanlist(ctx)
	if err != nil {
		return err
	}
	s.setKnownBanned(fullBanlist)
	return nil
}

func (s *defaultBanService) rememberKnownBanned(ctx context.Context, userID int64, provider string) {
	s.markKnownBanned(userID)
	if s.db == nil {
		return
	}
	seenAt := time.Now().UTC()
	expiresAt := seenAt.Add(banlistOnlineTTL)
	if err := s.db.ApplyBanlistSource(
		ctx,
		provider,
		banlistFeedOnline,
		newBanlistGeneration(),
		[]int64{userID},
		seenAt,
		&expiresAt,
		false,
	); err != nil {
		log.WithFields(log.Fields{
			logFieldUserID:   userID,
			logFieldProvider: provider,
			logFieldError:    err.Error(),
		}).Error("failed to persist online banned user")
		return
	}
	if err := s.loadKnownBannedFromDB(ctx); err != nil {
		log.WithFields(log.Fields{
			logFieldUserID:   userID,
			logFieldProvider: provider,
			logFieldError:    err.Error(),
		}).Error("failed to reload online banned user")
	}
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
	if isTelegramPrivilegeError(err) {
		return ErrNoPrivileges
	}
	return fmt.Errorf("failed to %s user: %w", operation, err)
}

func isTelegramPrivilegeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoPrivileges) {
		return true
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range []string{
		strings.ToUpper(MsgNoPrivileges),
		"CHAT_ADMIN_REQUIRED",
		"NOT ENOUGH RIGHTS",
		"BOT IS NOT AN ADMINISTRATOR",
		"NEED ADMINISTRATOR RIGHTS",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
