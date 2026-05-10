package handlers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const reactionProfileMessagesLimit = 10

func (r *Reactor) handleMessageReaction(ctx context.Context, reaction *api.MessageReactionUpdated, chat *api.Chat, settings *db.Settings) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method":     "handleMessageReaction",
		"messageID":  reaction.MessageID,
		"chat_id":    chat.ID,
		"chat_title": chat.Title,
	})

	if settings != nil && !settings.ReactionModerationEnabled {
		entry.Debug("reaction moderation is disabled")
		return true, nil
	}
	if !r.hasNewFlaggedReaction(reaction) {
		return true, nil
	}

	switch {
	case reaction.User != nil:
		if err := r.moderateReactionUser(ctx, reaction, chat, reaction.User, entry); err != nil {
			return true, err
		}
	case reaction.ActorChat != nil:
		if err := r.moderateReactionActorChat(ctx, chat, reaction.ActorChat, entry); err != nil {
			return true, err
		}
	default:
		entry.Debug("reaction update has no user or actor chat")
	}

	return true, nil
}

func (r *Reactor) handleReactionCount(_ context.Context, reactions *api.MessageReactionCountUpdated, _ *api.Chat, settings *db.Settings) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method":    "handleReactionCount",
		"messageID": reactions.MessageID,
	})
	if settings != nil && !settings.ReactionModerationEnabled {
		entry.Debug("reaction moderation is disabled")
		return true, nil
	}

	flaggedCount := 0
	for _, react := range reactions.Reactions {
		emoji := r.getEmojiFromReaction(react.Type)
		if slices.Contains(r.config.FlaggedEmojis, emoji) {
			flaggedCount += react.TotalCount
		}
	}

	entry.WithField("flaggedCount", flaggedCount).Debug("Counted flagged reactions")
	return true, nil
}

func (r *Reactor) hasNewFlaggedReaction(reaction *api.MessageReactionUpdated) bool {
	if reaction == nil {
		return false
	}
	oldFlagged := map[string]struct{}{}
	for _, react := range reaction.OldReaction {
		emoji := r.getEmojiFromReaction(react)
		if slices.Contains(r.config.FlaggedEmojis, emoji) {
			oldFlagged[emoji] = struct{}{}
		}
	}
	for _, react := range reaction.NewReaction {
		emoji := r.getEmojiFromReaction(react)
		if !slices.Contains(r.config.FlaggedEmojis, emoji) {
			continue
		}
		if _, ok := oldFlagged[emoji]; !ok {
			return true
		}
	}
	return false
}

func (r *Reactor) moderateReactionUser(ctx context.Context, reaction *api.MessageReactionUpdated, chat *api.Chat, user *api.User, entry *log.Entry) error {
	entry = entry.WithFields(log.Fields{
		"actor_type": "user",
		"actor_id":   user.ID,
		"username":   user.UserName,
	})

	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		return fmt.Errorf("check reaction user membership: %w", err)
	}
	if isMember {
		entry.Debug("skipping reaction moderation for known member")
		return nil
	}

	member, err := r.s.GetBot().GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: chat.ID},
			UserID:     user.ID,
		},
	})
	if err == nil && !member.HasLeft() && !member.WasKicked() {
		if insertErr := r.s.InsertMember(ctx, chat.ID, user.ID); insertErr != nil {
			entry.WithError(insertErr).Warn("failed to remember reaction user as member")
		}
		entry.Debug("skipping reaction moderation for Telegram-confirmed member")
		return nil
	}
	if err != nil {
		entry.WithError(err).Debug("failed to check reaction user chat membership, continuing as external reactor")
	}

	isBanned, err := r.banService.CheckBan(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("check reaction user banlist: %w", err)
	}
	if isBanned {
		return r.punishReactionUser(ctx, chat.ID, reaction.MessageID, user.ID, entry)
	}

	profileText := r.buildReactionUserProfileText(user, entry)
	if strings.TrimSpace(profileText) == "" {
		entry.Debug("reaction user profile has no moderation signal")
		return nil
	}

	isSpam, err := r.spamDetector.IsSpam(ctx, profileText, nil)
	if err != nil {
		return fmt.Errorf("check reaction user profile spam: %w", err)
	}
	if isSpam == nil || !*isSpam {
		entry.Debug("reaction user profile is not spam")
		return nil
	}

	return r.punishReactionUser(ctx, chat.ID, reaction.MessageID, user.ID, entry)
}

func (r *Reactor) moderateReactionActorChat(ctx context.Context, chat *api.Chat, actorChat *api.Chat, entry *log.Entry) error {
	entry = entry.WithFields(log.Fields{
		"actor_type": "chat",
		"actor_id":   actorChat.ID,
		"username":   actorChat.UserName,
	})
	profileText := r.buildReactionActorChatProfileText(actorChat, entry)
	if strings.TrimSpace(profileText) == "" {
		entry.Debug("reaction actor chat profile has no moderation signal")
		return nil
	}

	isSpam, err := r.spamDetector.IsSpam(ctx, profileText, nil)
	if err != nil {
		return fmt.Errorf("check reaction actor chat profile spam: %w", err)
	}
	if isSpam == nil || !*isSpam {
		entry.Debug("reaction actor chat profile is not spam")
		return nil
	}

	if _, err := r.s.GetBot().DeleteAllMessageReactions(api.DeleteAllMessageReactionsConfig{
		ChatConfig:  api.ChatConfig{ChatID: chat.ID},
		ActorChatID: actorChat.ID,
	}); err != nil {
		return fmt.Errorf("delete all actor chat reactions: %w", err)
	}
	if _, err := r.s.GetBot().Request(api.BanChatSenderChatConfig{
		ChatConfig:   api.ChatConfig{ChatID: chat.ID},
		SenderChatID: actorChat.ID,
	}); err != nil {
		return fmt.Errorf("ban reaction sender chat: %w", err)
	}
	entry.Info("Successfully banned reaction sender chat")
	return nil
}

func (r *Reactor) punishReactionUser(ctx context.Context, chatID int64, messageID int, userID int64, entry *log.Entry) error {
	if _, err := r.s.GetBot().DeleteAllMessageReactions(api.DeleteAllMessageReactionsConfig{
		ChatConfig: api.ChatConfig{ChatID: chatID},
		UserID:     userID,
	}); err != nil {
		return fmt.Errorf("delete all user reactions: %w", err)
	}
	if err := bot.BanUserFromChat(ctx, r.s.GetBot(), userID, chatID, 0); err != nil {
		return fmt.Errorf("ban reaction user: %w", err)
	}
	entry.WithField("messageID", messageID).Info("Successfully banned reaction user")
	return nil
}

func (r *Reactor) buildReactionUserProfileText(user *api.User, entry *log.Entry) string {
	parts := []string{
		"Reaction author profile:",
		"Name: " + bot.GetFullName(user),
	}
	if user.UserName != "" {
		parts = append(parts, "Username: @"+user.UserName)
	}

	info, err := r.s.GetBot().GetChat(api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: user.ID}})
	if err != nil {
		entry.WithError(err).Debug("failed to get reaction user profile")
	} else {
		parts = append(parts, chatProfileText(info)...)
		if info.PersonalChat != nil {
			parts = append(parts, "Personal chat: "+chatShortText(*info.PersonalChat))
		}
	}

	messages, err := r.s.GetBot().GetUserPersonalChatMessages(api.UserPersonalChatMessagesConfig{
		UserID: user.ID,
		Limit:  reactionProfileMessagesLimit,
	})
	if err != nil {
		entry.WithError(err).Debug("failed to get reaction user personal chat messages")
	} else {
		for _, msg := range messages {
			content := bot.ExtractContentFromMessage(&msg)
			if content != "" {
				parts = append(parts, "Personal chat message: "+content)
			}
		}
	}

	if len(parts) <= 2 && user.UserName == "" {
		return ""
	}
	return strings.Join(parts, "\n")
}

func (r *Reactor) buildReactionActorChatProfileText(actorChat *api.Chat, entry *log.Entry) string {
	parts := []string{
		"Reaction actor chat profile:",
	}
	if short := chatShortText(*actorChat); short != "" {
		parts = append(parts, short)
	}

	info, err := r.s.GetBot().GetChat(api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: actorChat.ID}})
	if err != nil {
		entry.WithError(err).Debug("failed to get reaction actor chat profile")
	} else {
		parts = append(parts, chatProfileText(info)...)
	}

	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func chatShortText(chat api.Chat) string {
	parts := []string{}
	if chat.Title != "" {
		parts = append(parts, "Title: "+chat.Title)
	}
	if chat.FirstName != "" || chat.LastName != "" {
		parts = append(parts, "Name: "+strings.TrimSpace(chat.FirstName+" "+chat.LastName))
	}
	if chat.UserName != "" {
		parts = append(parts, "Username: @"+chat.UserName)
	}
	return strings.Join(parts, ", ")
}

func chatProfileText(info api.ChatFullInfo) []string {
	parts := []string{}
	if short := chatShortText(info.Chat); short != "" {
		parts = append(parts, short)
	}
	if len(info.ActiveUsernames) > 0 {
		parts = append(parts, "Active usernames: @"+strings.Join(info.ActiveUsernames, ", @"))
	}
	if info.Bio != "" {
		parts = append(parts, "Bio: "+info.Bio)
	}
	if info.Description != "" {
		parts = append(parts, "Description: "+info.Description)
	}
	if info.PinnedMessage != nil {
		if content := bot.ExtractContentFromMessage(info.PinnedMessage); content != "" {
			parts = append(parts, "Pinned message: "+content)
		}
	}
	return parts
}

func (r *Reactor) getEmojiFromReaction(react api.ReactionType) string {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method": "getEmojiFromReaction",
	})

	if !react.IsCustomEmoji() {
		return react.Emoji
	}

	emojiStickers, err := r.s.GetBot().GetCustomEmojiStickers(api.GetCustomEmojiStickersConfig{
		CustomEmojiIDs: []string{react.CustomEmoji},
	})
	if err != nil {
		entry.WithField("error", err.Error()).Error("Failed to get custom emoji sticker")
		return react.Emoji
	}
	if len(emojiStickers) == 0 {
		entry.Debug("custom emoji sticker lookup returned no stickers")
		return react.Emoji
	}
	return emojiStickers[0].Emoji
}
