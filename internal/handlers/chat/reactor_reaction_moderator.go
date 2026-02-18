package handlers

import (
	"context"
	"fmt"
	"slices"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	log "github.com/sirupsen/logrus"
)

func (r *Reactor) handleReaction(ctx context.Context, reactions *api.MessageReactionCountUpdated, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method":    "handleReaction",
		"messageID": reactions.MessageID,
	})

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

		if err := bot.DeleteChatMessage(ctx, r.s.GetBot(), chat.ID, reactions.MessageID); err != nil {
			entry.WithField("error", err.Error()).WithField("chat_title", chat.Title).Error("Failed to delete message")
		}

		if err := bot.BanUserFromChat(ctx, r.s.GetBot(), user.ID, chat.ID, 0); err != nil {
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
