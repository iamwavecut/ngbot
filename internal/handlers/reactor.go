package handlers

import (
	"context"
	"slices"

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

	if err := r.validateUpdate(u, chat, user); err != nil {
		return false, err
	}

	settings, err := r.getOrCreateSettings(chat)
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

func (r *Reactor) getOrCreateSettings(chat *api.Chat) (*db.Settings, error) {
	settings, err := r.s.GetSettings(chat.ID)
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
		if err := r.s.SetSettings(settings); err != nil {
			return nil, err
		}
	}
	return settings, nil
}

func (r *Reactor) handleReaction(_ context.Context, reactions *api.MessageReactionCountUpdated, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithField("method", "handleReaction")

	for _, react := range reactions.Reactions {
		if react.TotalCount < 5 {
			continue
		}
		emoji := r.getEmojiFromReaction(react.Type)
		if slices.Contains(r.config.FlaggedEmojis, emoji) {
			entry.Warn("user reached flag threshold, attempting to ban")
			if err := bot.BanUserFromChat(r.s.GetBot(), user.ID, chat.ID); err != nil {
				entry.WithError(err).Error("failed to ban user")
				return true, err
			}
			return true, nil
		}
	}
	return true, nil
}

func (r *Reactor) getEmojiFromReaction(react api.ReactionType) string {
	if react.Type != api.StickerTypeCustomEmoji {
		return react.Emoji
	}

	emojiStickers, err := r.s.GetBot().GetCustomEmojiStickers(api.GetCustomEmojiStickersConfig{
		CustomEmojiIDs: []string{react.CustomEmoji},
	})
	if err != nil || len(emojiStickers) == 0 {
		return react.Emoji
	}
	return emojiStickers[0].Emoji
}

func (r *Reactor) handleMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	entry := r.getLogEntry().WithField("method", "handleMessage")

	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		return errors.Wrap(err, "failed to check membership")
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
