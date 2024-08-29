package bot

import (
	"context"
	"sync"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/sirupsen/logrus"
)

type ServiceBot interface {
	GetBot() *api.BotAPI
}

type ServiceDB interface {
	GetDB() db.Client
}

type Service interface {
	ServiceBot
	ServiceDB
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
	InsertMember(ctx context.Context, chatID, userID int64) error
	GetSettings(chatID int64) (*db.Settings, error)
	SetSettings(settings *db.Settings) error
	Shutdown(ctx context.Context) error
}

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
	go s.warmupCache()
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
		return false, ctx.Err()
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

		isMember, err := s.dbClient.IsMember(chatID, userID)
		if err != nil {
			return false, err
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
		err := s.dbClient.InsertMember(chatID, userID)
		if err != nil {
			return err
		}

		s.cacheMutex.Lock()
		defer s.cacheMutex.Unlock()
		s.memberCache[chatID] = append(s.memberCache[chatID], userID)

		return nil
	}
}

func (s *service) GetSettings(chatID int64) (*db.Settings, error) {
	s.cacheMutex.RLock()
	if settings, ok := s.settingsCache[chatID]; ok {
		s.cacheMutex.RUnlock()
		return settings, nil
	}
	s.cacheMutex.RUnlock()

	settings, err := s.dbClient.GetSettings(chatID)
	if err != nil {
		return nil, err
	}

	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	s.settingsCache[chatID] = settings

	return settings, nil
}

func (s *service) SetSettings(settings *db.Settings) error {
	err := s.dbClient.SetSettings(settings)
	if err != nil {
		return err
	}

	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	s.settingsCache[settings.ID] = settings

	return nil
}

func (s *service) warmupCache() {
	members, err := s.dbClient.GetAllMembers()
	if err != nil {
		s.getLogEntry().WithError(err).Error("Failed to warmup member cache")
		return
	}
	s.cacheMutex.Lock()
	for chatID, userIDs := range members {
		s.memberCache[chatID] = append(s.memberCache[chatID], userIDs...)
	}
	s.cacheMutex.Unlock()

	settings, err := s.dbClient.GetAllSettings()
	if err != nil {
		s.getLogEntry().WithError(err).Error("Failed to warmup settings cache")
		return
	}
	s.cacheMutex.Lock()
	for chatID, setting := range settings {
		s.settingsCache[chatID] = setting
	}
	s.cacheMutex.Unlock()

	s.getLogEntry().WithFields(logrus.Fields{
		"memberCount":   len(members),
		"settingsCount": len(settings),
	}).Info("Cache warmed up successfully")
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
