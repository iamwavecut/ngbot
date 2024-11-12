package handlers

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pkg/errors"
	"github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
)

// SpamDetector handles spam detection logic
type SpamDetectorInterface interface {
	IsSpam(ctx context.Context, message string) (bool, error)
}

// Config holds reactor configuration
type Config struct {
	FlaggedEmojis   []string
	CheckUserAPIURL string
	OpenAIModel     string
	SpamControl     config.SpamControl
}

// Reactor handles message processing and spam detection
type Reactor struct {
	s            bot.Service
	llmAPI       *openai.Client
	config       Config
	spamDetector SpamDetectorInterface
	banService   BanService
	spamControl  *SpamControl
}

// NewReactor creates a new Reactor instance with the given configuration
func NewReactor(s bot.Service, llmAPI *openai.Client, banService BanService, spamControl *SpamControl, spamDetector SpamDetectorInterface, config Config) *Reactor {
	r := &Reactor{
		s:            s,
		llmAPI:       llmAPI,
		config:       config,
		banService:   banService,
		spamControl:  spamControl,
		spamDetector: spamDetector,
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

	if u.CallbackQuery != nil {
		return r.handleCallbackQuery(ctx, u, chat, user)
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

func (r *Reactor) handleCallbackQuery(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{"method": "handleCallbackQuery"})
	if !strings.HasPrefix(u.CallbackQuery.Data, "spam_vote:") {
		return true, nil
	}

	parts := strings.Split(u.CallbackQuery.Data, ":")
	if len(parts) != 3 {
		return true, nil
	}

	caseID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return true, nil
	}

	vote := parts[2] == "0" // 0 = not spam, 1 = spam

	err = r.s.GetDB().AddSpamVote(ctx, &db.SpamVote{
		CaseID:  caseID,
		VoterID: user.ID,
		Vote:    vote,
		VotedAt: time.Now(),
	})
	if err != nil {
		entry.WithError(err).Error("failed to add spam vote")
	}

	votes, err := r.s.GetDB().GetSpamVotes(ctx, caseID)
	if err != nil {
		entry.WithError(err).Error("failed to get votes")
		return true, nil
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

	text := fmt.Sprintf(i18n.Get("Votes: ✅ %d | 🚫 %d", r.getLanguage(ctx, chat, user)), notSpamVotes, spamVotes)

	edit := api.NewEditMessageText(chat.ID, u.CallbackQuery.Message.MessageID, text)
	edit.ReplyMarkup = u.CallbackQuery.Message.ReplyMarkup
	if _, err := r.s.GetBot().Send(edit); err != nil {
		entry.WithError(err).Error("failed to update vote count")
	}

	_, err = r.s.GetBot().Request(api.NewCallback(u.CallbackQuery.ID, i18n.Get("✓ Vote recorded", r.getLanguage(ctx, chat, user))))
	if err != nil {
		entry.WithError(err).Error("failed to acknowledge callback")
	}

	if max(notSpamVotes, spamVotes) >= r.config.SpamControl.MaxVoters {
		if err := r.spamControl.resolveCase(ctx, caseID); err != nil {
			entry.WithError(err).Error("failed to resolve spam case after max votes reached")
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
	entry := r.getLogEntry().WithFields(log.Fields{
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
	entry := r.getLogEntry().WithFields(log.Fields{
		"method": "getEmojiFromReaction",
	})

	if react.Type != api.StickerTypeCustomEmoji {
		return react.Emoji
	}

	emojiStickers, err := r.s.GetBot().GetCustomEmojiStickers(api.GetCustomEmojiStickersConfig{
		CustomEmojiIDs: []string{react.CustomEmoji},
	})
	if err != nil || len(emojiStickers) == 0 {
		entry.WithError(err).Error("Failed to get custom emoji sticker")
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
		if err := r.spamControl.ProcessSpamMessage(ctx, msg, true, r.getLanguage(ctx, chat, user)); err != nil {
			entry.WithError(err).Error("failed to process spam message")
			// Fallback to direct ban if spam control fails
			if err := r.banService.BanUser(ctx, chat.ID, user.ID, msg.MessageID); err != nil {
				entry.WithError(err).Error("failed to ban user")
			}
		}
		return true, nil
	}

	return false, nil
}

func (r *Reactor) getLogEntry() *log.Entry {
	return log.WithField("object", "Reactor")
}

func (r *Reactor) getLanguage(ctx context.Context, chat *api.Chat, user *api.User) string {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method": "getLanguage",
		"chatID": chat.ID,
	})
	entry.Debug("Entering method")

	if settings, err := r.s.GetDB().GetSettings(ctx, chat.ID); !tool.Try(err) {
		entry.Debug("Using language from chat settings")
		return settings.Language
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		entry.Debug("Using language from user settings")
		return user.LanguageCode
	}
	entry.Debug("Using default language")

	entry.Debug("Exiting method")
	return config.Get().DefaultLanguage
}
