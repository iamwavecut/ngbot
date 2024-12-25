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
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
)

// SpamDetector handles spam detection logic
type SpamDetectorInterface interface {
	IsSpam(ctx context.Context, message string) (*bool, error)
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
	config       Config
	spamDetector SpamDetectorInterface
	banService   BanService
	spamControl  *SpamControl
}

// NewReactor creates a new Reactor instance with the given configuration
func NewReactor(s bot.Service, banService BanService, spamControl *SpamControl, spamDetector SpamDetectorInterface, config Config) *Reactor {
	r := &Reactor{
		s:            s,
		config:       config,
		banService:   banService,
		spamControl:  spamControl,
		spamDetector: spamDetector,
	}
	r.getLogEntry().Debug("created new reactor")
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
		if u.Message.IsCommand() {
			if err := r.handleCommand(ctx, u.Message, chat, user); err != nil {
				entry.WithField("error", err.Error()).Error("error handling message")
				return true, err
			}
			return true, nil
		}
		if err := r.handleMessage(ctx, u.Message, chat, user); err != nil {
			entry.WithField("error", err.Error()).Error("error handling message")
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
		entry.WithField("error", err.Error()).Error("failed to add spam vote")
	}

	votes, err := r.s.GetDB().GetSpamVotes(ctx, caseID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get votes")
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

	language := r.s.GetLanguage(ctx, chat.ID, user)

	text := fmt.Sprintf(i18n.Get("Votes: ✅ %d | 🚫 %d", language), notSpamVotes, spamVotes)

	edit := api.NewEditMessageText(chat.ID, u.CallbackQuery.Message.MessageID, text)
	edit.ReplyMarkup = u.CallbackQuery.Message.ReplyMarkup
	if _, err := r.s.GetBot().Send(edit); err != nil {
		entry.WithField("error", err.Error()).Error("failed to update vote count")
	}

	_, err = r.s.GetBot().Request(api.NewCallback(u.CallbackQuery.ID, i18n.Get("✓ Vote recorded", language)))
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to acknowledge callback")
	}

	if max(notSpamVotes, spamVotes) >= r.config.SpamControl.MaxVoters {
		if err := r.spamControl.resolveCase(ctx, caseID); err != nil {
			entry.WithField("error", err.Error()).Error("failed to resolve spam case after max votes reached")
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
			entry.WithField("error", err.Error()).WithField("chat_title", chat.Title).Error("Failed to delete message")
		}

		if err := bot.BanUserFromChat(ctx, r.s.GetBot(), user.ID, chat.ID); err != nil {
			entry.WithField("error", err.Error()).WithField("chat_title", chat.Title).Error("Failed to ban user")
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
		entry.WithField("error", err.Error()).Error("Failed to get custom emoji sticker")
		return react.Emoji
	}
	return emojiStickers[0].Emoji
}

func (r *Reactor) handleCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	switch msg.Command() {
	case "/testspam":
		return r.testSpamCommand(ctx, msg, chat, user)
	}

	return nil
}

func (r *Reactor) testSpamCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	content := msg.CommandArguments()

	isSpam, err := r.checkMessageForSpam(ctx, content, user)
	if err != nil {
		return errors.Wrap(err, "failed to check message for spam")
	}
	responseMsg := api.NewMessage(chat.ID, fmt.Sprintf("Is spam: %t", *isSpam))
	responseMsg.ReplyParameters.AllowSendingWithoutReply = true
	responseMsg.ReplyParameters.MessageID = msg.MessageID
	responseMsg.ReplyParameters.ChatID = chat.ID
	responseMsg.MessageThreadID = msg.MessageThreadID
	_, _ = r.s.GetBot().Send(responseMsg)

	return nil
}

func (r *Reactor) handleMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method": "handleMessage",
	})

	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("Failed to check membership")
		return fmt.Errorf("failed to check membership: %w", err)
	}
	if isMember {
		return nil
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	isBanned, err := r.banService.CheckBan(ctx, user.ID)
	if err != nil {
		return errors.Wrap(err, "failed to check ban")
	}
	if isBanned {
		if r.config.SpamControl.DebugUserID != 0 {
			debugMsg := tool.ExecTemplate(`Banned user: {{ .user_name }} ({{ .user_id }})`, map[string]any{
				"user_name": bot.GetUN(user),
				"user_id":   user.ID,
			})
			_, _ = r.s.GetBot().Send(api.NewMessage(r.config.SpamControl.DebugUserID, debugMsg))
		}
		if err := r.spamControl.ProcessBannedMessage(ctx, msg, chat, language); err != nil {
			entry.WithField("error", err.Error()).Error("failed to process banned message")
		}
		return nil
	}

	content := bot.ExtractContentFromMessage(msg)
	if content == "" {
		entry.WithField("message", msg).Warn("empty message content")
		return nil
	}
	isSpam, err := r.checkMessageForSpam(ctx, content, user)
	if err != nil {
		return errors.Wrap(err, "failed to check message for spam")
	}
	if isSpam == nil {
		return nil
	}
	if *isSpam {
		if err := r.spamControl.ProcessSpamMessage(ctx, msg, chat, language); err != nil {
			entry.WithField("error", err.Error()).Error("failed to process spam message")
		}
		return nil
	}

	if err := r.s.InsertMember(ctx, chat.ID, user.ID); err != nil {
		return errors.Wrap(err, "failed to insert member")
	}

	return nil
}

func (r *Reactor) checkMessageForSpam(ctx context.Context, content string, user *api.User) (*bool, error) {
	words := strings.Fields(content)
	for i, word := range words {
		if hasCyrillics(word) {
			words[i] = normalizeCyrillics(word)
		}
	}
	contentAltered := strings.Join(words, " ")

	isSpam, err := r.spamDetector.IsSpam(ctx, contentAltered)
	if r.config.SpamControl.DebugUserID != 0 {
		debugMsg := tool.ExecTemplate(`
{{- .content }}

---
Is Spam result: {{ .isSpam -}}
`, map[string]any{
			"content": content,
			// "contentAltered": contentAltered,
			"isSpam": isSpam,
		})
		_, _ = r.s.GetBot().Send(api.NewMessage(r.config.SpamControl.DebugUserID, debugMsg))
	}

	return isSpam, err
}

func (r *Reactor) getLogEntry() *log.Entry {
	return log.WithField("object", "Reactor")
}

func hasCyrillics(content string) bool {
	return strings.ContainsAny(strings.ToLower(content), "абвгдеёжзийклмнопрстуфхцчшщъыьэюя")
}

// normalizeCyrillic replaces all non-cyrillic fake characters with their real counterparts
func normalizeCyrillics(content string) string {
	return tool.Strtr(content, map[string]string{
		"a": "а",
		"e": "е",
		"o": "о",
		"p": "р",
		"c": "с",
		"y": "у",
		"x": "х",
		"u": "и",
		"A": "А",
		"b": "в",
		"B": "В",
		"C": "С",
		"d": "д",
		"D": "Д",
		"E": "Е",
		"g": "г",
		"G": "Г",
		"h": "н",
		"H": "Н",
		"i": "і",
		"I": "І",
		"j": "ј",
		"J": "Ј",
		"k": "к",
		"K": "К",
		"m": "м",
		"M": "М",
		"n": "п",
		"N": "П",
		"O": "О",
		"P": "Р",
		"ԛ": "q",
		"г": "r",
		"Г": "R",
		"ѕ": "s",
		"Ѕ": "S",
		"т": "t",
		"T": "Т",
		"U": "И",
		"ѵ": "v",
		"Ѵ": "V",
		"X": "Х",
		"Y": "У",
		"w": "ш",
		"W": "Ш",
		"z": "з",
		"Z": "З",
		"ᴧ": "л",
		"ʙ": "в",
		"ᴦ": "г",
		"ɸ": "ф",
		"ᴛ": "т",
		"ᴇ": "е",
		"ᴩ": "р",
		"ᴀ": "а",
		"ᴋ": "к",
		"ᴁ": "ае",
		"ᴂ": "а",
		"ᴃ": "в",
		"ᴄ": "с",
		"ᴅ": "д",
		"ᴆ": "д",
		"ᴈ": "з",
		"ᴉ": "и",
		"ᴊ": "й",
		"ᴌ": "л",
		"ᴍ": "м",
		"ᴎ": "н",
		"ᴏ": "о",
		"ᴐ": "о",
		"ᴑ": "о",
		"ᴒ": "о",
		"ᴓ": "о",
		"ᴔ": "о",
		"ᴕ": "о",
		"ᴖ": "о",
		"ᴗ": "о",
		"ᴘ": "п",
		"ᴙ": "я",
		"ᴚ": "р",
		"ᴜ": "у",
		"ᴝ": "у",
		"ᴞ": "у",
		"ᴟ": "у",
		"ᴠ": "в",
		"ᴡ": "ш",
		"ᴢ": "з",
		"ᴣ": "з",
		"ᴤ": "с",
		"ᴥ": "я",
		"ᴨ": "п",
		"ᴪ": "п",
		"ᴫ": "л",
		"ᴬ": "А",
		"ᴭ": "А",
		"ᴮ": "В",
		"ᴯ": "В",
		"ᴰ": "Д",
		"ᴱ": "Е",
		"ᴲ": "Е",
		"ᴳ": "Г",
		"ᴴ": "Н",
		"ᴵ": "І",
		"ᴶ": "Й",
		"ᴷ": "К",
		"ᴸ": "Л",
		"ᴹ": "М",
		"ᴺ": "Н",
		"ᴻ": "Н",
		"ᴼ": "О",
		"ᴽ": "О",
		"ᴾ": "Р",
		"ᴿ": "Р",
		"α": "а",
		"β": "в",
		"γ": "г",
		"δ": "д",
		"ε": "е",
		"ζ": "з",
		"η": "н",
		"θ": "о",
		"ι": "и",
		"κ": "к",
		"λ": "л",
		"μ": "м",
		"ν": "н",
		"ξ": "к",
		"ο": "о",
		"π": "п",
		"ρ": "р",
		"σ": "с",
		"τ": "т",
		"υ": "у",
		"φ": "ф",
		"χ": "х",
		"ψ": "п",
		"ω": "о",
		"Α": "А",
		"Β": "В",
		"Γ": "Г",
		"Δ": "Д",
		"Ε": "Е",
		"Ζ": "З",
		"Η": "Н",
		"Θ": "О",
		"Ι": "І",
		"Κ": "К",
		"Λ": "Л",
		"Μ": "М",
		"Ν": "Н",
		"Ξ": "К",
		"Ο": "О",
		"Π": "П",
		"Ρ": "Р",
		"Σ": "С",
		"Τ": "Т",
		"Υ": "У",
		"Φ": "Ф",
		"Χ": "Х",
		"Ψ": "П",
		"Ω": "О",
	})
}
