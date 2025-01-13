package handlers

import (
	"context"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
	log "github.com/sirupsen/logrus"
)

// SpamService combines spam detection and control functionality
type SpamService struct {
	service    bot.Service
	config     config.SpamControl
	banService BanService
	llm        adapters.LLM
	logger     *log.Entry
}

// NewSpamService creates a new spam service
func NewSpamService(service bot.Service, config config.SpamControl, banService BanService, llm adapters.LLM) *SpamService {
	return &SpamService{
		service:    service,
		config:     config,
		banService: banService,
		llm:        llm,
		logger:     log.WithField("service", "spam"),
	}
}

// IsSpam checks if a message is spam
func (s *SpamService) IsSpam(ctx context.Context, message string) (*bool, error) {
	entry := s.logger.WithField("method", "IsSpam")
	entry.Debug("checking if message is spam")

	result, err := s.llm.Detect(ctx, message)
	if err != nil {
		entry.WithError(err).Error("failed to detect spam")
		return nil, err
	}

	return result, nil
}

// ProcessSpamMessage handles a detected spam message
func (s *SpamService) ProcessSpamMessage(ctx context.Context, msg *api.Message, chat *api.Chat, language string) error {
	entry := s.logger.WithField("method", "ProcessSpamMessage")
	entry.Debug("processing spam message")

	spamCase, err := s.service.GetDB().CreateSpamCase(ctx, &db.SpamCase{
		ChatID:      chat.ID,
		UserID:      msg.From.ID,
		MessageText: msg.Text,
		CreatedAt:   time.Now(),
		Status:      "pending",
	})
	if err != nil {
		entry.WithError(err).Error("failed to create spam case")
		return err
	}

	// Create vote message
	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonData("✅ "+i18n.Get("Not Spam", language), fmt.Sprintf("spam_vote:%d:0", spamCase.ID)),
			api.NewInlineKeyboardButtonData("🚫 "+i18n.Get("Spam", language), fmt.Sprintf("spam_vote:%d:1", spamCase.ID)),
		),
	)

	voteMsg := api.NewMessage(chat.ID, fmt.Sprintf(
		i18n.Get("Is this message spam?\n\nFrom: %s\nMessage: %s", language),
		bot.GetUN(msg.From),
		msg.Text,
	))
	voteMsg.ReplyMarkup = keyboard

	sent, err := s.service.GetBot().Send(voteMsg)
	if err != nil {
		entry.WithError(err).Error("failed to send vote message")
		return err
	}

	// Update the spam case with the notification message ID
	spamCase.NotificationMessageID = sent.MessageID
	if err := s.service.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		entry.WithError(err).Error("failed to update spam case with notification message ID")
	}

	return nil
}

// ProcessBannedMessage handles a message from a banned user
func (s *SpamService) ProcessBannedMessage(ctx context.Context, msg *api.Message, chat *api.Chat, language string) error {
	entry := s.logger.WithField("method", "ProcessBannedMessage")
	entry.Debug("processing banned message")

	if err := bot.DeleteChatMessage(ctx, s.service.GetBot(), chat.ID, msg.MessageID); err != nil {
		entry.WithError(err).Error("failed to delete message")
		return err
	}

	if err := s.banService.BanUserWithMessage(ctx, chat.ID, msg.From.ID, msg.MessageID); err != nil {
		entry.WithError(err).Error("failed to ban user")
		return err
	}

	if s.config.DebugUserID != 0 {
		debugMsg := tool.ExecTemplate(`Banned message from: {{ .user_name }} ({{ .user_id }})`, map[string]any{
			"user_name": bot.GetUN(msg.From),
			"user_id":   msg.From.ID,
		})
		_, _ = s.service.GetBot().Send(api.NewMessage(s.config.DebugUserID, debugMsg))
	}

	return nil
}

// ResolveCase resolves a spam case based on votes
func (s *SpamService) ResolveCase(ctx context.Context, caseID int64) error {
	entry := s.logger.WithField("method", "ResolveCase")
	entry.Debug("resolving spam case")

	spamCase, err := s.service.GetDB().GetSpamCase(ctx, caseID)
	if err != nil {
		entry.WithError(err).Error("failed to get spam case")
		return err
	}

	votes, err := s.service.GetDB().GetSpamVotes(ctx, caseID)
	if err != nil {
		entry.WithError(err).Error("failed to get votes")
		return err
	}

	spamVotes := 0
	notSpamVotes := 0
	for _, v := range votes {
		if v.Vote {
			notSpamVotes++
		} else {
			spamVotes++
		}
	}

	now := time.Now()
	if spamVotes > notSpamVotes {
		spamCase.Status = "spam"
		if err := s.banService.BanUserWithMessage(ctx, spamCase.ChatID, spamCase.UserID, spamCase.NotificationMessageID); err != nil {
			entry.WithError(err).Error("failed to ban user")
			return err
		}
	} else {
		spamCase.Status = "false_positive"
	}
	spamCase.ResolvedAt = &now

	if err := s.service.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		entry.WithError(err).Error("failed to update spam case")
		return err
	}

	return nil
} 
