package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/policy/permissions"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

func (r *Reactor) handleCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	if !commandTargetsCurrentBot(msg, r.bot.Self.UserName) {
		return nil
	}

	switch msg.Command() {
	case "testspam":
		if !r.diagnosticCommandAllowed(ctx, chat, user) {
			return r.rejectDiagnosticCommand(ctx, msg)
		}
		return r.testSpamCommand(ctx, msg, chat)
	case "skipreason":
		if !r.diagnosticCommandAllowed(ctx, chat, user) {
			return r.rejectDiagnosticCommand(ctx, msg)
		}
		return r.skipReasonCommand(ctx, msg, chat)
	case "voteban":
		return r.voteBanCommand(ctx, msg, chat, user, settings)
	}

	return nil
}

func (r *Reactor) diagnosticCommandAllowed(ctx context.Context, chat *api.Chat, user *api.User) bool {
	if user == nil {
		return false
	}
	if r.config.SpamControl.DebugUserID != 0 && user.ID == r.config.SpamControl.DebugUserID {
		return true
	}
	if chat == nil || chat.Type == telegramChatTypePrivate {
		return false
	}
	member, err := bot.GetChatMember(ctx, r.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: chat.ID},
			UserID:     user.ID,
		},
	})
	if err != nil {
		r.getLogEntry().WithError(err).Warn("failed to authorize diagnostic command")
		return false
	}
	return member.IsCreator() || member.IsAdministrator()
}

func (r *Reactor) rejectDiagnosticCommand(ctx context.Context, msg *api.Message) error {
	err := r.sendTemporaryReply(ctx, msg, "This diagnostic command is restricted to chat administrators.")
	return err
}

func commandTargetsCurrentBot(msg *api.Message, botUserName string) bool {
	if msg == nil || !msg.IsCommand() {
		return false
	}

	commandWithAt := msg.CommandWithAt()
	if commandWithAt == "" {
		return false
	}

	_, after, ok := strings.Cut(commandWithAt, "@")
	if !ok {
		return true
	}
	if botUserName == "" {
		return false
	}

	return strings.EqualFold(after, botUserName)
}

func messageMentionsCurrentBot(msg *api.Message, self api.User) bool {
	if msg == nil {
		return false
	}
	if self.UserName == "" && self.ID == 0 {
		return false
	}
	if messageEntitiesMentionCurrentBot(msg.Text, msg.Entities, self) {
		return true
	}
	return messageEntitiesMentionCurrentBot(msg.Caption, msg.CaptionEntities, self)
}

func messageEntitiesMentionCurrentBot(text string, entities []api.MessageEntity, self api.User) bool {
	for _, entity := range entities {
		if entity.IsMention() {
			mention := strings.TrimPrefix(entityText(text, entity), "@")
			if self.UserName != "" && strings.EqualFold(mention, self.UserName) {
				return true
			}
			continue
		}
		if entity.IsTextMention() && entity.User != nil {
			if self.ID != 0 && entity.User.ID == self.ID {
				return true
			}
			if self.UserName != "" && strings.EqualFold(entity.User.UserName, self.UserName) {
				return true
			}
		}
	}
	return false
}

func entityText(text string, entity api.MessageEntity) string {
	encoded := utf16.Encode([]rune(text))
	if entity.Offset < 0 || entity.Length <= 0 || entity.Offset >= len(encoded) {
		return ""
	}
	end := entity.Offset + entity.Length
	if end > len(encoded) {
		return ""
	}
	return string(utf16.Decode(encoded[entity.Offset:end]))
}

func (r *Reactor) testSpamCommand(ctx context.Context, msg *api.Message, chat *api.Chat) error {
	content := msg.CommandArguments()

	isSpam, err := r.checkMessageForSpam(ctx, chat.ID, content)
	if err != nil {
		return errors.Wrap(err, "failed to check message for spam")
	}
	responseMsg := api.NewMessage(chat.ID, fmt.Sprintf("Is spam: %t", *isSpam))
	responseMsg.ReplyParameters.AllowSendingWithoutReply = true
	responseMsg.ReplyParameters.MessageID = msg.MessageID
	responseMsg.ReplyParameters.ChatID = chat.ID
	responseMsg.MessageThreadID = msg.MessageThreadID
	_, _ = bot.Send(ctx, r.bot, responseMsg)

	return nil
}

func (r *Reactor) skipReasonCommand(ctx context.Context, msg *api.Message, chat *api.Chat) error {
	if msg.ReplyToMessage == nil {
		responseMsg := api.NewMessage(chat.ID, "Please reply to a message to see its skip reason")
		responseMsg.ReplyParameters.MessageID = msg.MessageID
		responseMsg.ReplyParameters.ChatID = chat.ID
		responseMsg.MessageThreadID = msg.MessageThreadID
		_, _ = bot.Send(ctx, r.bot, responseMsg)
		return nil
	}

	result := r.GetLastProcessingResult(chat.ID, msg.ReplyToMessage.MessageID)
	if result == nil {
		responseMsg := api.NewMessage(chat.ID, "No processing information available for this message")
		responseMsg.ReplyParameters.MessageID = msg.MessageID
		responseMsg.ReplyParameters.ChatID = chat.ID
		responseMsg.MessageThreadID = msg.MessageThreadID
		_, _ = bot.Send(ctx, r.bot, responseMsg)
		return nil
	}

	var response string
	if result.Skipped {
		response = fmt.Sprintf("Message was skipped at stage %s\nReason: %s", result.Stage, result.SkipReason)
	} else if result.IsSpam != nil {
		response = fmt.Sprintf("Message was processed through stage %s\nSpam check result: %v", result.Stage, *result.IsSpam)
	} else {
		response = fmt.Sprintf("Message processing stopped at stage %s", result.Stage)
	}

	responseMsg := api.NewMessage(chat.ID, response)
	responseMsg.ReplyParameters.MessageID = msg.MessageID
	responseMsg.ReplyParameters.ChatID = chat.ID
	if msg.Chat.IsForum {
		responseMsg.MessageThreadID = msg.MessageThreadID
	}
	_, _ = bot.Send(ctx, r.bot, responseMsg)

	return nil
}

func (r *Reactor) voteBanCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	entry := r.getLogEntry().WithFields(log.Fields{
		logFieldMethod: "voteBanCommand",
		"chatID":       chat.ID,
		"userID":       user.ID,
	})

	if msg.Chat.Type == telegramChatTypePrivate {
		_ = r.sendTemporaryReply(ctx, msg, i18n.Get("This command can only be used in groups", r.s.GetLanguage(ctx, chat.ID, user)))
		return nil
	}
	moderationAvailable, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		entry.WithError(err).Warn("failed to inspect moderation rights; skipping report")
	}
	if err != nil || !moderationAvailable {
		_ = r.sendTemporaryReply(ctx, msg, i18n.Get("Moderation is unavailable because I do not have permission to restrict members.", r.s.GetLanguage(ctx, chat.ID, user)))
		return nil
	}

	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		_ = r.sendTemporaryReply(ctx, msg, i18n.Get("Use /voteban or mention me in reply to a spam message to start a vote.", r.s.GetLanguage(ctx, chat.ID, user)))
		return nil
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	target := msg.ReplyToMessage
	if r.banService != nil {
		isBanlisted := r.banService.IsKnownBanned(target.From.ID)
		if !isBanlisted {
			isBanlisted, err = r.banService.CheckBan(ctx, target.From.ID)
			if err != nil {
				return errors.Wrap(err, "failed to check reported user banlist")
			}
		}
		if isBanlisted {
			outcome := enforceBanlistedMessage(ctx, r.bot, r.banService, target, chat, target.From)
			if outcome.err != nil {
				entry.WithError(outcome.err).Error("failed to enforce terminal banlist action for reported user")
			}
			if outcome.userBanned {
				if err := r.s.DeleteMember(ctx, chat.ID, target.From.ID); err != nil {
					entry.WithError(err).Error("failed to forget directly banned member")
				}
				_ = r.sendTemporaryReply(ctx, msg, i18n.Get("Reported message was confirmed as spam. The user was banned.", language))
			}
			r.deleteReportMessage(ctx, msg)
			return nil
		}
	}
	isReportedSpam, err := r.checkReportedMessageForSpam(ctx, chat.ID, bot.ExtractContentFromMessage(target))
	if err != nil {
		entry.WithError(err).Warn("reported spam LLM check failed; falling back to report flow")
	}
	if isReportedSpam != nil && *isReportedSpam {
		result, err := r.processBanned(ctx, target, chat, language)
		if err != nil {
			entry.WithError(err).Error("Failed to process spam message")
			return errors.Wrap(err, "failed to process spam message")
		}
		if err := r.s.DeleteMember(ctx, chat.ID, target.From.ID); err != nil {
			entry.WithError(err).Error("Failed to delete member")
		}
		if result != nil && result.UserBanned {
			_ = r.sendTemporaryReply(ctx, msg, i18n.Get("Reported message was confirmed as spam. The user was banned.", language))
		}
		r.deleteReportMessage(ctx, msg)
		return nil
	}

	if r.reporterCanRestrictMembers(ctx, chat.ID, user.ID) {
		_, err := r.processBanned(ctx, target, chat, language)
		if err != nil {
			entry.WithError(err).Error("Failed to process spam message")
			return errors.Wrap(err, "failed to process spam message")
		}
		if err := r.s.DeleteMember(ctx, chat.ID, target.From.ID); err != nil {
			entry.WithError(err).Error("Failed to delete member")
		}
		r.deleteReportMessage(ctx, msg)
		return nil
	}

	if settings != nil && !settings.CommunityVotingEnabled {
		_ = r.sendTemporaryReply(ctx, msg, i18n.Get("Community voting is disabled", language))
		return nil
	}

	if _, err := r.processReported(ctx, target, msg, chat, language); err != nil {
		entry.WithError(err).Error("Failed to process spam message")
		return errors.Wrap(err, "failed to process spam message")
	}

	return nil
}

func (r *Reactor) reporterCanRestrictMembers(ctx context.Context, chatID int64, userID int64) bool {
	member, err := bot.GetChatMember(ctx, r.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
	})
	if err != nil {
		r.getLogEntry().WithError(err).WithField("chatID", chatID).WithField("userID", userID).Warn("failed to get reporter chat member; treating as non-restrict reporter")
		return false
	}
	return permissions.CanRestrictMembers(&member)
}

func (r *Reactor) sendTemporaryReply(ctx context.Context, msg *api.Message, text string) error {
	responseMsg := api.NewMessage(msg.Chat.ID, text)
	responseMsg.ReplyParameters.MessageID = msg.MessageID
	responseMsg.ReplyParameters.ChatID = msg.Chat.ID
	responseMsg.ReplyParameters.AllowSendingWithoutReply = true
	responseMsg.MessageThreadID = msg.MessageThreadID
	responseMsg.DisableNotification = true
	responseMsg.LinkPreviewOptions.IsDisabled = true
	sent, err := bot.Send(ctx, r.bot, responseMsg)
	if err != nil {
		return err
	}
	if r.spamControl != nil && sent.MessageID != 0 {
		r.spamControl.DeleteMessageAfter(msg.Chat.ID, sent.MessageID, time.Minute)
	}
	return nil
}

func (r *Reactor) deleteReportMessage(ctx context.Context, msg *api.Message) {
	if msg == nil {
		return
	}
	if err := bot.DeleteChatMessage(ctx, r.bot, msg.Chat.ID, msg.MessageID); err != nil {
		r.getLogEntry().WithError(err).WithField("chatID", msg.Chat.ID).WithField("messageID", msg.MessageID).Debug("failed to delete report message")
	}
}
