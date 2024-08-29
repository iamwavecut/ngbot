package bot

import (
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
	IsMember(chatID, userID int64) (bool, error)
	InsertMember(chatID, userID int64) error
}

type service struct {
	bot             *api.BotAPI
	db              db.Client
	memberCache     map[int64][]int64
	cacheMutex      sync.RWMutex
	cacheExpiration time.Duration
	log             *logrus.Entry
}

func NewService(bot *api.BotAPI, db db.Client, log *logrus.Entry) *service {
	s := &service{
		bot:             bot,
		db:              db,
		memberCache:     make(map[int64][]int64),
		cacheExpiration: 5 * time.Minute,
		log:             log,
	}
	go s.warmupCache()
	return s
}

func (s *service) GetBot() *api.BotAPI {
	return s.bot
}

func (s *service) GetDB() db.Client {
	return s.db
}

func (s *service) IsMember(chatID, userID int64) (bool, error) {
	if chatMembers, ok := s.memberCache[chatID]; ok {
		for _, member := range chatMembers {
			if member == userID {
				return true, nil
			}
		}
	}

	isMember, err := s.db.IsMember(chatID, userID)
	if err != nil {
		return false, err
	}

	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	s.memberCache[chatID] = append(s.memberCache[chatID], userID)

	return isMember, nil
}

func (s *service) InsertMember(chatID, userID int64) error {
	err := s.db.InsertMember(chatID, userID)
	if err != nil {
		return err
	}

	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	s.memberCache[chatID] = append(s.memberCache[chatID], userID)

	return nil
}

func (s *service) warmupCache() {
	members, err := s.db.GetAllMembers()
	if err != nil {
		s.getLogEntry().WithError(err).Error("Failed to warmup cache")
		return
	}
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	for chatID, userIDs := range members {
		s.memberCache[chatID] = append(s.memberCache[chatID], userIDs...)
	}
	s.getLogEntry().WithField("memberCount", len(members)).Info("Cache warmed up successfully")
}

func (s *service) getLogEntry() *logrus.Entry {
	return s.log.WithField("object", "Service")
}
