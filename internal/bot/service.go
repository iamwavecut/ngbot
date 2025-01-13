package bot

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"golang.org/x/sync/errgroup"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type service struct {
	bot             *api.BotAPI
	dbClient        db.Client
	memberCache     map[int64][]int64
	settingsCache   map[int64]*db.Settings
	cacheMutex      sync.RWMutex
	cacheExpiration time.Duration
	log             *logrus.Entry
	ctx             context.Context
	cancel          context.CancelFunc
}

func NewService(ctx context.Context, bot *api.BotAPI, dbClient db.Client, log *logrus.Entry) *service {
	ctx, cancel := context.WithCancel(ctx)
	s := &service{
		bot:             bot,
		dbClient:        dbClient,
		memberCache:     make(map[int64][]int64),
		settingsCache:   make(map[int64]*db.Settings),
		cacheExpiration: 5 * time.Minute,
		log:             log,
		ctx:             ctx,
		cancel:          cancel,
	}

	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()

		if err := s.warmupCache(ctx); err != nil {
			s.log.WithField("errorv", fmt.Sprintf("%+v", err)).Error("Failed to warm up cache")
		}
	}()
	return s
}

func (s *service) GetBot() *api.BotAPI {
	return s.bot
}

func (s *service) GetDB() db.Client {
	return s.dbClient
}

func (s *service) IsMember(ctx context.Context, chatID, userID int64) (bool, error) {
	select {
	case <-ctx.Done():
		return false, fmt.Errorf("context cancelled: %w", ctx.Err())
	default:
		s.cacheMutex.RLock()
		if chatMembers, ok := s.memberCache[chatID]; ok {
			for _, member := range chatMembers {
				if member == userID {
					s.cacheMutex.RUnlock()
					return true, nil
				}
			}
		}
		s.cacheMutex.RUnlock()

		isMember, err := s.dbClient.IsMember(ctx, chatID, userID)
		if err != nil {
			return false, fmt.Errorf("failed to check membership: %w", err)
		}

		s.cacheMutex.Lock()
		defer s.cacheMutex.Unlock()
		s.memberCache[chatID] = append(s.memberCache[chatID], userID)

		return isMember, nil
	}
}

func (s *service) InsertMember(ctx context.Context, chatID, userID int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		err := s.dbClient.InsertMember(ctx, chatID, userID)
		if err != nil {
			return err
		}

		s.cacheMutex.Lock()
		defer s.cacheMutex.Unlock()
		s.memberCache[chatID] = append(s.memberCache[chatID], userID)

		return nil
	}
}

func (s *service) GetSettings(ctx context.Context, chatID int64) (*db.Settings, error) {
	s.cacheMutex.RLock()
	if settings, ok := s.settingsCache[chatID]; ok {
		s.cacheMutex.RUnlock()
		return settings, nil
	}
	s.cacheMutex.RUnlock()

	settings, err := s.dbClient.GetSettings(ctx, chatID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			settings = &db.Settings{
				ID:               chatID,
				Enabled:          true,
				ChallengeTimeout: (3 * time.Minute).Nanoseconds(),
				RejectTimeout:    (10 * time.Minute).Nanoseconds(),
				Language:         "en",
			}
			if err := s.SetSettings(ctx, settings); err != nil {
				return nil, fmt.Errorf("error setting default settings: %w", err)
			}
		} else {
			return nil, fmt.Errorf("error fetching settings from database: %w", err)
		}
	}

	s.cacheMutex.Lock()
	s.settingsCache[chatID] = settings
	s.cacheMutex.Unlock()

	return settings, nil
}

func (s *service) SetSettings(ctx context.Context, settings *db.Settings) error {
	err := s.dbClient.SetSettings(ctx, settings)
	if err != nil {
		return err
	}

	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	s.settingsCache[settings.ID] = settings

	return nil
}

func (s *service) warmupCache(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		members, err := s.dbClient.GetAllMembers(ctx)
		if err != nil {
			return fmt.Errorf("failed to warmup member cache: %w", err)
		}
		s.cacheMutex.Lock()
		for chatID, userIDs := range members {
			s.memberCache[chatID] = append(s.memberCache[chatID], userIDs...)
		}
		s.cacheMutex.Unlock()
		return nil
	})

	g.Go(func() error {
		settings, err := s.dbClient.GetAllSettings(ctx)
		if err != nil {
			return fmt.Errorf("failed to warmup settings cache: %w", err)
		}
		s.cacheMutex.Lock()
		for chatID, setting := range settings {
			s.settingsCache[chatID] = setting
		}
		s.cacheMutex.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	membersCount := 0
	for _, userIDs := range s.memberCache {
		membersCount += len(userIDs)
	}

	s.getLogEntry().WithFields(logrus.Fields{
		"membersChats":  len(s.memberCache),
		"membersCount":  membersCount,
		"settingsCount": len(s.settingsCache),
	}).Info("Cache warmed up successfully")
	return nil
}

func (s *service) getLogEntry() *logrus.Entry {
	return s.log.WithField("object", "Service")
}

func (s *service) Shutdown(ctx context.Context) error {
	s.cancel()
	s.dbClient.Close()
	s.memberCache = nil
	s.settingsCache = nil
	return nil
}

func (s *service) GetLanguage(ctx context.Context, chatID int64, user *api.User) string {
	if settings, err := s.GetSettings(ctx, chatID); err == nil && settings != nil {
		return settings.Language
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		return user.LanguageCode
	}
	return config.Get().DefaultLanguage
}
