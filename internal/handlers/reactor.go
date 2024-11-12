package handlers

import (
	"context"
	"fmt"
	"slices"
	"time"

	api "github.com/OvyFlash/telegram-bot-api/v6"
	"github.com/pkg/errors"
	"github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
)

// SpamDetector handles spam detection logic
type SpamDetector interface {
	IsSpam(ctx context.Context, message string) (bool, error)
}

// BanService handles user banning operations
type BanService interface {
	CheckBan(ctx context.Context, userID int64) (bool, error)
	BanUser(ctx context.Context, chatID, userID int64, messageID int) error
}

// Config holds reactor configuration
type Config struct {
	FlaggedEmojis   []string
	CheckUserAPIURL string
	OpenAIModel     string
}

// Reactor handles message processing and spam detection
type Reactor struct {
	s            bot.Service
	llmAPI       *openai.Client
	config       Config
	spamDetector SpamDetector
	banService   BanService
}

// NewReactor creates a new Reactor instance with the given configuration
func NewReactor(s bot.Service, llmAPI *openai.Client, config Config) *Reactor {
	r := &Reactor{
		s:      s,
		llmAPI: llmAPI,
		config: config,
	}

	r.spamDetector = &openAISpamDetector{
		client: llmAPI,
		model:  config.OpenAIModel,
	}
	r.banService = &defaultBanService{
		apiURL: config.CheckUserAPIURL,
		bot:    s.GetBot(),
	}

	return r
}

func (r *Reactor) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{"method": "Handle"})
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	if err := r.validateUpdate(u, chat, user); err != nil {
		return false, err
	}

	settings, err := r.getOrCreateSettings(ctx, chat)
	if err != nil {
		return false, err
	}

	if !settings.Enabled {
		entry.Debug("reactor is disabled for this chat")
		return true, nil
	}

	if u.MessageReactionCount != nil {
		return r.handleReaction(ctx, u.MessageReactionCount, chat, user)
	}

	if u.Message != nil {
		if err := r.handleMessage(ctx, u.Message, chat, user); err != nil {
			entry.WithError(err).Error("error handling message")
			return true, err
		}
	}

	return true, nil
}

func (r *Reactor) validateUpdate(u *api.Update, chat *api.Chat, user *api.User) error {
	if u == nil {
		return errors.New("nil update")
	}
	if u.Message == nil && u.MessageReaction == nil {
		return nil
	}
	if chat == nil || user == nil {
		return errors.New("nil chat or user")
	}
	return nil
}

func (r *Reactor) getOrCreateSettings(ctx context.Context, chat *api.Chat) (*db.Settings, error) {
	settings, err := r.s.GetSettings(ctx, chat.ID)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		settings = &db.Settings{
			Enabled:          true,
			ChallengeTimeout: defaultChallengeTimeout.Nanoseconds(),
			RejectTimeout:    defaultRejectTimeout.Nanoseconds(),
			Language:         "en",
			ID:               chat.ID,
		}
		if err := r.s.SetSettings(ctx, settings); err != nil {
			return nil, err
		}
	}
	return settings, nil
}

func (r *Reactor) handleReaction(ctx context.Context, reactions *api.MessageReactionCountUpdated, chat *api.Chat, user *api.User) (bool, error) {
	entry := log.WithContext(ctx).WithFields(log.Fields{
		"method":    "handleReaction",
		"messageID": reactions.MessageID,
	})

	// Track flagged emoji count
	flaggedCount := 0
	for _, react := range reactions.Reactions {
		emoji := r.getEmojiFromReaction(react.Type)
		if slices.Contains(r.config.FlaggedEmojis, emoji) {
			flaggedCount += react.TotalCount
		}
	}

	entry.WithField("flaggedCount", flaggedCount).Debug("Counted flagged reactions")

	if flaggedCount >= 5 {
		entry.Warn("User reached flag threshold, attempting to ban")

		// Add context to API calls
		if err := bot.DeleteChatMessage(ctx, r.s.GetBot(), chat.ID, reactions.MessageID); err != nil {
			entry.WithError(err).Error("Failed to delete message")
		}

		if err := bot.BanUserFromChat(ctx, r.s.GetBot(), user.ID, chat.ID); err != nil {
			entry.WithError(err).Error("Failed to ban user")
			return true, fmt.Errorf("failed to ban user: %w", err)
		}

		entry.Info("Successfully banned user due to reactions")
		return true, nil
	}

	return true, nil
}

func (r *Reactor) getEmojiFromReaction(react api.ReactionType) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if react.Type != api.StickerTypeCustomEmoji {
		return react.Emoji
	}

	emojiStickers, err := r.s.GetBot().GetCustomEmojiStickers(api.GetCustomEmojiStickersConfig{
		CustomEmojiIDs: []string{react.CustomEmoji},
	})
	if err != nil || len(emojiStickers) == 0 {
		log.WithContext(ctx).WithError(err).Error("Failed to get custom emoji sticker")
		return react.Emoji
	}
	return emojiStickers[0].Emoji
}

func (r *Reactor) handleMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method":     "handleMessage",
		"chat_id":    chat.ID,
		"user_id":    user.ID,
		"message_id": msg.MessageID,
	})

	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		entry.WithError(err).Error("Failed to check membership")
		return fmt.Errorf("failed to check membership: %w", err)
	}
	if isMember {
		entry.Debug("user is already a member")
		return nil
	}

	isSpam, err := r.checkMessageForSpam(ctx, msg, chat, user)
	if err != nil {
		return errors.Wrap(err, "failed to check message for spam")
	}
	if isSpam {
		entry.Info("message detected as spam")
		return nil
	}

	if err := r.s.InsertMember(ctx, chat.ID, user.ID); err != nil {
		return errors.Wrap(err, "failed to insert member")
	}

	entry.Debug("successfully added user as member")
	return nil
}

func (r *Reactor) checkMessageForSpam(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method":    "checkMessageForSpam",
		"user_name": bot.GetUN(user),
		"user_id":   user.ID,
	})

	content := msg.Text
	if content == "" {
		content = msg.Caption
	}
	if content == "" {
		entry.Debug("empty message content")
		return false, nil
	}

	isBanned, err := r.banService.CheckBan(ctx, user.ID)
	if err != nil {
		return false, err
	}
	if isBanned {
		if err := r.banService.BanUser(ctx, chat.ID, user.ID, msg.MessageID); err != nil {
			entry.WithError(err).Error("failed to ban user")
		}
		return true, nil
	}

	isSpam, err := r.spamDetector.IsSpam(ctx, content)
	if err != nil {
		return false, err
	}
	if isSpam {
		if err := r.banService.BanUser(ctx, chat.ID, user.ID, msg.MessageID); err != nil {
			entry.WithError(err).Error("failed to ban user")
		}
		return true, nil
	}

	return false, nil
}

func (r *Reactor) getLogEntry() *log.Entry {
	return log.WithField("object", "Reactor")
}
