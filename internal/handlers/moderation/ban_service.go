package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/policy/permissions"
	log "github.com/sirupsen/logrus"
)

const (
	MsgNoPrivileges = "not enough rights to restrict/unrestrict chat member"

	banlistURL       = "https://lols.bot/spam/banlist.txt"
	banlistURLHourly = "https://lols.bot/spam/banlist-1h.txt"
	scammersURL      = "https://lols.bot/scammers.txt"
	casCheckURL      = "https://api.cas.chat/check?user_id=%d"

	accoutsAPIURLTemplate = "https://api.lols.bot/account?id=%v"

	banServiceHTTPTimeout         = 10 * time.Second
	banlistDownloadTimeout        = 2 * time.Minute
	banServiceMaxRetries          = 3
	banServiceRetryStep           = 300 * time.Millisecond
	banlistHourlyTTL              = 26 * time.Hour
	banlistOnlineTTL              = 24 * time.Hour
	banlistShrinkThreshold        = 0.8
	banlistShrinkConfirmDelay     = time.Hour
	banlistObservationPruneBatch  = 10_000
	banlistOnlineSourceSuffix     = "/online"
	banlistMinParseCapacity       = 1_024
	banlistSizeHintSlackDivisor   = 64
	onlineBanQueueCapacity        = 1_024
	moderationStatusTTL           = 5 * time.Minute
	banServiceMaintenanceInterval = time.Hour
	casKeyRejectedLogInterval     = 10 * time.Minute
	casKeyRejectedLogMessage      = "CAS API key rejected; CAS online checks fail open"

	retainedRecordsCleanupBatchSize = 500

	onlineCheckerLoLs = "lols"
	onlineCheckerCAS  = "cas"

	logFieldProvider = "provider"
	logFieldSource   = "source"
	logFieldChatID   = "chat_id"
	logFieldUserID   = "user_id"
	logFieldError    = "error"
)

type BanService interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	CheckBan(ctx context.Context, userID int64) (bool, error)
	MuteUser(ctx context.Context, chatID, userID int64, until time.Time) error
	UnmuteUser(ctx context.Context, chatID, userID int64) error
	BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error
	UnbanUser(ctx context.Context, chatID, userID int64) error
	IsRestricted(ctx context.Context, chatID, userID int64) (bool, error)
	IsKnownBanned(userID int64) bool
	ModerationAvailable(ctx context.Context, chatID int64) (bool, error)
	MarkModerationUnavailable(chatID int64)
}

type banStore interface {
	DeleteAuthorMessageContext(ctx context.Context, chatID int64, author db.MessageAuthor) error
	BanlistSnapshots(ctx context.Context) ([]db.BanlistSnapshot, error)
	SaveBanlistSnapshot(ctx context.Context, snapshot *db.BanlistSnapshot) error
	TouchBanlistSnapshot(ctx context.Context, source string, etag, lastModified sql.NullString, checkedAt time.Time) (bool, error)
	BanlistObservations(ctx context.Context, now time.Time) ([]db.BanlistObservation, error)
	UpsertBanlistObservations(ctx context.Context, observations []db.BanlistObservation) error
	DeleteExpiredBanlistObservations(ctx context.Context, now time.Time, limit int) (int64, error)
	CleanupRetainedRecords(ctx context.Context, now time.Time, limit int) error
	AddRestriction(ctx context.Context, restriction *db.UserRestriction) error
	EnsureRestrictionSnapshot(ctx context.Context, restriction *db.UserRestriction) (*db.UserRestriction, error)
	RemoveRestriction(ctx context.Context, chatID int64, userID int64) error
	GetActiveRestriction(ctx context.Context, chatID, userID int64) (*db.UserRestriction, error)
	GetRestriction(ctx context.Context, chatID, userID int64) (*db.UserRestriction, error)
	RemoveExpiredRestrictions(ctx context.Context) error
}

type (
	BanServiceOptions struct {
		CASAPIKey string
	}

	onlineBanChecker struct {
		name  string
		check func(ctx context.Context, client *http.Client, userID int64) (bool, error)
	}

	defaultBanService struct {
		bot            *api.BotAPI
		db             banStore
		httpClient     *http.Client
		downloadClient *http.Client
		sources        []banlistSourceSpec
		onlineCheckers []onlineBanChecker
		now            func() time.Time

		index        *banlistIndex
		observations map[int64]time.Time
		mapMutex     sync.RWMutex

		refreshMutex   sync.Mutex
		sourceStates   map[string]banlistSourceState
		pendingShrinks map[string]pendingBanlistShrink

		moderation     map[int64]moderationStatus
		moderationM    sync.RWMutex
		onlineBanQueue chan db.BanlistObservation

		casRejectionMutex    sync.Mutex
		casRejectionLoggedAt time.Time
		casRejections        int

		runMutex            sync.Mutex
		started             bool
		runCancel           context.CancelFunc
		workersWg           sync.WaitGroup
		maintenanceInterval time.Duration
	}

	moderationStatus struct {
		available bool
		expiresAt time.Time
	}
)

var (
	ErrNoPrivileges   = fmt.Errorf("no privileges")
	errCASKeyRejected = errors.New("CAS API key rejected")
)

func NewBanService(bot *api.BotAPI, store banStore, options BanServiceOptions) BanService {
	return &defaultBanService{
		bot:                 bot,
		db:                  store,
		httpClient:          &http.Client{Timeout: banServiceHTTPTimeout},
		downloadClient:      &http.Client{Timeout: banlistDownloadTimeout},
		sources:             defaultBanlistSources(),
		onlineCheckers:      defaultOnlineBanCheckers(options.CASAPIKey),
		now:                 time.Now,
		moderation:          map[int64]moderationStatus{},
		onlineBanQueue:      make(chan db.BanlistObservation, onlineBanQueueCapacity),
		maintenanceInterval: banServiceMaintenanceInterval,
	}
}

func (s *defaultBanService) ModerationAvailable(ctx context.Context, chatID int64) (bool, error) {
	now := time.Now()
	s.moderationM.RLock()
	status, ok := s.moderation[chatID]
	s.moderationM.RUnlock()
	if ok && now.Before(status.expiresAt) {
		return status.available, nil
	}
	if s.bot == nil || s.bot.Self.ID == 0 {
		return false, errors.New("bot identity is unavailable")
	}

	member, err := bot.GetChatMember(ctx, s.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     s.bot.Self.ID,
		},
	})
	if err != nil {
		if isTelegramPrivilegeError(err) {
			s.MarkModerationUnavailable(chatID)
			return false, nil
		}
		return false, fmt.Errorf("check bot moderation rights: %w", err)
	}

	available := permissions.CanRestrictMembers(&member)
	s.moderationM.Lock()
	if s.moderation == nil {
		s.moderation = map[int64]moderationStatus{}
	}
	s.moderation[chatID] = moderationStatus{available: available, expiresAt: now.Add(moderationStatusTTL)}
	s.moderationM.Unlock()
	return available, nil
}

func (s *defaultBanService) MarkModerationUnavailable(chatID int64) {
	s.moderationM.Lock()
	if s.moderation == nil {
		s.moderation = map[int64]moderationStatus{}
	}
	s.moderation[chatID] = moderationStatus{available: false, expiresAt: time.Now().Add(moderationStatusTTL)}
	s.moderationM.Unlock()
}

func defaultOnlineBanCheckers(casAPIKey string) []onlineBanChecker {
	checkers := []onlineBanChecker{{
		name: onlineCheckerLoLs,
		check: func(ctx context.Context, client *http.Client, userID int64) (bool, error) {
			return checkLoLsBan(ctx, client, accoutsAPIURLTemplate, userID)
		},
	}}
	if casAPIKey != "" {
		checkers = append(checkers, onlineBanChecker{
			name: onlineCheckerCAS,
			check: func(ctx context.Context, client *http.Client, userID int64) (bool, error) {
				return checkCASBan(ctx, client, casCheckURL, casAPIKey, userID)
			},
		})
	}
	return checkers
}

func (s *defaultBanService) Start(ctx context.Context) error {
	s.runMutex.Lock()
	defer s.runMutex.Unlock()
	if s.started {
		return nil
	}
	if err := s.loadBanlists(ctx); err != nil {
		return fmt.Errorf("load banlists: %w", err)
	}
	if err := s.cleanupExpiredRestrictions(ctx); err != nil {
		return err
	}
	if err := s.cleanupRetainedRecords(ctx, s.currentTime().UTC()); err != nil {
		return fmt.Errorf("clean retained records: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.runCancel = cancel
	s.workersWg.Go(func() {
		s.persistOnlineBanObservations(runCtx)
	})
	s.workersWg.Go(func() {
		s.runMaintenance(runCtx)
	})

	s.started = true
	return nil
}

func (s *defaultBanService) runMaintenance(ctx context.Context) {
	interval := s.maintenanceInterval
	if interval <= 0 {
		interval = banServiceMaintenanceInterval
	}
	startedAt := s.currentTime()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.refreshDueSources(ctx, startedAt)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.currentTime()
			if err := s.cleanupExpiredRestrictions(ctx); err != nil && !errorsIsCanceled(err) {
				log.WithError(err).Error("Failed to clean up expired restrictions")
			}
			if err := s.cleanupRetainedRecords(ctx, now.UTC()); err != nil && !errorsIsCanceled(err) {
				log.WithError(err).Error("Failed to clean retained records")
			}
			s.pruneObservations(ctx, now)
			s.refreshDueSources(ctx, now)
		}
	}
}

func (s *defaultBanService) cleanupRetainedRecords(ctx context.Context, now time.Time) error {
	if s.db == nil {
		return nil
	}
	return s.db.CleanupRetainedRecords(ctx, now, retainedRecordsCleanupBatchSize)
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

func (s *defaultBanService) IsKnownBanned(userID int64) bool {
	now := s.currentTime()
	s.mapMutex.RLock()
	defer s.mapMutex.RUnlock()
	if s.index.contains(userID) {
		return true
	}
	expiresAt, observed := s.observations[userID]
	return observed && now.Before(expiresAt)
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

	checkers := s.onlineCheckers
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
	for _, checker := range checkers {
		go func() {
			banned, err := checker.check(checkCtx, s.httpClient, userID)
			results <- checkResult{
				provider: checker.name,
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
			if errors.Is(result.err, errCASKeyRejected) {
				s.reportCASKeyRejected()
				continue
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

func checkCASBan(ctx context.Context, client *http.Client, urlTemplate, apiKey string, userID int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(urlTemplate, userID), nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("%w: status %d", errCASKeyRejected, resp.StatusCode)
	case resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices:
		return false, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	var banInfo struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&banInfo); err != nil {
		return false, fmt.Errorf("failed to decode response: %w", err)
	}
	return banInfo.OK && len(banInfo.Result) > 0 && string(banInfo.Result) != "null", nil
}

func (s *defaultBanService) reportCASKeyRejected() {
	now := s.currentTime()
	s.casRejectionMutex.Lock()
	s.casRejections++
	if !s.casRejectionLoggedAt.IsZero() && now.Sub(s.casRejectionLoggedAt) < casKeyRejectedLogInterval {
		s.casRejectionMutex.Unlock()
		return
	}
	rejections := s.casRejections
	s.casRejections = 0
	s.casRejectionLoggedAt = now
	s.casRejectionMutex.Unlock()
	log.WithField("rejections", rejections).Error(casKeyRejectedLogMessage)
}

func (s *defaultBanService) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *defaultBanService) rememberKnownBanned(ctx context.Context, userID int64, provider string) {
	now := s.currentTime()
	observation := db.BanlistObservation{
		UserID:     userID,
		Source:     provider + banlistOnlineSourceSuffix,
		ObservedAt: now,
		ExpiresAt:  now.Add(banlistOnlineTTL),
	}
	s.observe(userID, observation.ExpiresAt)
	if s.onlineBanQueue != nil {
		select {
		case s.onlineBanQueue <- observation:
			return
		default:
			log.WithField(logFieldUserID, userID).Warn("online ban persistence queue is full; persisting synchronously")
		}
	}
	s.persistKnownBanned(ctx, observation)
}

func (s *defaultBanService) persistOnlineBanObservations(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case observation := <-s.onlineBanQueue:
			s.persistKnownBanned(ctx, observation)
		}
	}
}

func (s *defaultBanService) persistKnownBanned(ctx context.Context, observation db.BanlistObservation) {
	if s.db == nil {
		return
	}
	if err := s.db.UpsertBanlistObservations(ctx, []db.BanlistObservation{observation}); err != nil {
		log.WithFields(log.Fields{
			logFieldUserID: observation.UserID,
			logFieldSource: observation.Source,
			logFieldError:  err.Error(),
		}).Error("failed to persist online banned user")
	}
}

func (s *defaultBanService) observe(userID int64, expiresAt time.Time) {
	s.mapMutex.Lock()
	defer s.mapMutex.Unlock()
	s.observeLocked(userID, expiresAt)
}

func (s *defaultBanService) observeAll(userIDs []int64, expiresAt time.Time) {
	s.mapMutex.Lock()
	defer s.mapMutex.Unlock()
	for _, userID := range userIDs {
		s.observeLocked(userID, expiresAt)
	}
}

func (s *defaultBanService) observeLocked(userID int64, expiresAt time.Time) {
	if s.observations == nil {
		s.observations = make(map[int64]time.Time)
	}
	if current, ok := s.observations[userID]; !ok || expiresAt.After(current) {
		s.observations[userID] = expiresAt
	}
}

func (s *defaultBanService) publishSource(source string, ids []int64) {
	s.mapMutex.Lock()
	defer s.mapMutex.Unlock()
	s.index = s.index.with(source, ids)
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
	return IsTelegramPrivilegeError(err)
}

func IsTelegramPrivilegeError(err error) bool {
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
