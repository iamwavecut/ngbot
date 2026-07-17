package handlers

import (
	"context"
	"fmt"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

const reactionProfileMessagesLimit = 10

func (r *Reactor) handleMessageReaction(ctx context.Context, reaction *api.MessageReactionUpdated, chat *api.Chat, settings *db.Settings) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{
		logFieldMethod: "handleMessageReaction",
		"messageID":    reaction.MessageID,
		logFieldChatID: chat.ID,
		"chat_title":   chat.Title,
	})

	if settings != nil && !settings.ReactionProfileCheckEnabled {
		entry.Debug("reaction profile check is disabled")
		return true, nil
	}
	if len(reaction.NewReaction) == 0 {
		return true, nil
	}
	moderationAvailable, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		entry.WithError(err).Warn("failed to inspect moderation rights; skipping reaction moderation")
	}
	if err != nil || !moderationAvailable {
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

func (r *Reactor) moderateReactionUser(ctx context.Context, reaction *api.MessageReactionUpdated, chat *api.Chat, user *api.User, entry *log.Entry) error {
	entry = entry.WithFields(log.Fields{
		"actor_type":     actorTypeUser,
		"actor_id":       user.ID,
		logFieldUsername: user.UserName,
	})

	isBanned, err := r.banService.CheckBan(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("check reaction user banlist: %w", err)
	}
	if isBanned {
		return r.punishReactionUser(ctx, chat.ID, reaction.MessageID, user.ID, entry)
	}

	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		return fmt.Errorf("check reaction user membership: %w", err)
	}
	if isMember {
		entry.Trace("skipping reaction profile check for known member")
		return nil
	}

	isKnownNonMember, err := r.store.IsChatKnownNonMember(ctx, chat.ID, user.ID)
	if err != nil {
		return fmt.Errorf("check reaction user known non-member state: %w", err)
	}
	if isKnownNonMember {
		entry.Trace("skipping reaction profile check for remembered non-member")
		return nil
	}

	member, err := bot.GetChatMember(ctx, r.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: chat.ID},
			UserID:     user.ID,
		},
	})
	if err == nil && !member.HasLeft() && !member.WasKicked() {
		entry.Debug("skipping reaction profile check without granting message trust")
		return nil
	}
	if err != nil {
		entry.WithError(err).Debug("failed to check reaction user chat membership, continuing as external reactor")
	}

	profileText := r.buildReactionUserProfileText(ctx, user, entry)
	if strings.TrimSpace(profileText) == "" {
		entry.Debug("reaction user profile has no moderation signal")
		return nil
	}

	isSpam, err := r.spamDetector.IsSpam(ctx, profileText, nil)
	if err != nil {
		return fmt.Errorf("check reaction user profile spam: %w", err)
	}
	if isSpam == nil {
		entry.Debug("reaction user profile spam check returned no decision")
		return nil
	}
	if !*isSpam {
		if upsertErr := r.store.UpsertChatKnownNonMember(ctx, &db.ChatKnownNonMember{
			ChatID: chat.ID,
			UserID: user.ID,
		}); upsertErr != nil {
			entry.WithError(upsertErr).Warn("failed to remember clean reaction user as non-member")
		}
		entry.Debug("reaction user profile is not spam")
		return nil
	}

	return r.punishReactionUser(ctx, chat.ID, reaction.MessageID, user.ID, entry)
}

func (r *Reactor) moderateReactionActorChat(ctx context.Context, chat *api.Chat, actorChat *api.Chat, entry *log.Entry) error {
	entry = entry.WithFields(log.Fields{
		"actor_type":     actorTypeChat,
		"actor_id":       actorChat.ID,
		logFieldUsername: actorChat.UserName,
	})
	if reason, trusted := r.trustedSenderChatIdentity(ctx, actorChat, chat, false, entry); trusted {
		entry.WithField("reason", reason).Debug("skipping trusted reaction actor chat")
		return nil
	}
	profileText := r.buildReactionActorChatProfileText(ctx, actorChat, entry)
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

	if _, err := bot.RequestBool(ctx, r.bot, api.DeleteAllMessageReactionsConfig{
		ChatConfig:  api.ChatConfig{ChatID: chat.ID},
		ActorChatID: actorChat.ID,
	}); err != nil {
		r.markModerationUnavailableOnPrivilege(chat.ID, err)
		return fmt.Errorf("delete all actor chat reactions: %w", err)
	}
	if _, err := r.bot.RequestWithContext(ctx, api.BanChatSenderChatConfig{
		ChatConfig:   api.ChatConfig{ChatID: chat.ID},
		SenderChatID: actorChat.ID,
	}); err != nil {
		r.markModerationUnavailableOnPrivilege(chat.ID, err)
		return fmt.Errorf("ban reaction sender chat: %w", err)
	}
	entry.Info("Successfully banned reaction sender chat")
	return nil
}

func (r *Reactor) punishReactionUser(ctx context.Context, chatID int64, messageID int, userID int64, entry *log.Entry) error {
	if _, err := bot.RequestBool(ctx, r.bot, api.DeleteAllMessageReactionsConfig{
		ChatConfig: api.ChatConfig{ChatID: chatID},
		UserID:     userID,
	}); err != nil {
		r.markModerationUnavailableOnPrivilege(chatID, err)
		return fmt.Errorf("delete all user reactions: %w", err)
	}
	if err := bot.BanUserFromChat(ctx, r.bot, userID, chatID, 0); err != nil {
		r.markModerationUnavailableOnPrivilege(chatID, err)
		return fmt.Errorf("ban reaction user: %w", err)
	}
	entry.WithField("messageID", messageID).Info("Successfully banned reaction user")
	return nil
}

func (r *Reactor) buildReactionUserProfileText(ctx context.Context, user *api.User, entry *log.Entry) string {
	parts := []string{
		"Reaction author profile:",
		"Name: " + bot.GetFullName(user),
	}
	if user.UserName != "" {
		parts = append(parts, "Username: @"+user.UserName)
	}

	info, err := bot.GetChat(ctx, r.bot, api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: user.ID}})
	if err != nil {
		entry.WithError(err).Debug("failed to get reaction user profile")
	} else {
		parts = append(parts, chatProfileText(info)...)
		if info.PersonalChat != nil {
			parts = append(parts, "Personal chat: "+chatShortText(*info.PersonalChat))
		}
	}

	messages, err := bot.GetUserPersonalChatMessages(ctx, r.bot, api.UserPersonalChatMessagesConfig{
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

func (r *Reactor) buildReactionActorChatProfileText(ctx context.Context, actorChat *api.Chat, entry *log.Entry) string {
	parts := []string{
		"Reaction actor chat profile:",
	}
	if short := chatShortText(*actorChat); short != "" {
		parts = append(parts, short)
	}

	info, err := bot.GetChat(ctx, r.bot, api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: actorChat.ID}})
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
