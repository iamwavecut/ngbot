package handlers

import (
	"context"
	"fmt"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/policy/permissions"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

func (r *Reactor) handleCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	switch msg.Command() {
	case "testspam":
		return r.testSpamCommand(ctx, msg, chat, user)
	case "skipreason":
		return r.skipReasonCommand(ctx, msg, chat)
	case "ban":
		return r.banCommand(ctx, msg, chat, user, settings)
	}

	return nil
}

func (r *Reactor) testSpamCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	content := msg.CommandArguments()

	isSpam, err := r.checkMessageForSpam(ctx, chat.ID, content, user)
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

func (r *Reactor) skipReasonCommand(ctx context.Context, msg *api.Message, chat *api.Chat) error {
	if msg.ReplyToMessage == nil {
		responseMsg := api.NewMessage(chat.ID, "Please reply to a message to see its skip reason")
		responseMsg.ReplyParameters.MessageID = msg.MessageID
		responseMsg.ReplyParameters.ChatID = chat.ID
		responseMsg.MessageThreadID = msg.MessageThreadID
		_, _ = r.s.GetBot().Send(responseMsg)
		return nil
	}

	result := r.GetLastProcessingResult(int64(msg.ReplyToMessage.MessageID))
	if result == nil {
		responseMsg := api.NewMessage(chat.ID, "No processing information available for this message")
		responseMsg.ReplyParameters.MessageID = msg.MessageID
		responseMsg.ReplyParameters.ChatID = chat.ID
		responseMsg.MessageThreadID = msg.MessageThreadID
		_, _ = r.s.GetBot().Send(responseMsg)
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
	_, _ = r.s.GetBot().Send(responseMsg)

	return nil
}

func (r *Reactor) banCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	entry := r.getLogEntry().WithFields(log.Fields{
		"method": "banCommand",
		"chatID": chat.ID,
		"userID": user.ID,
	})

	if msg.Chat.Type == "private" {
		responseMsg := api.NewMessage(chat.ID, i18n.Get("This command can only be used in groups", r.s.GetLanguage(ctx, chat.ID, user)))
		responseMsg.DisableNotification = true
		_, _ = r.s.GetBot().Send(responseMsg)
		return nil
	}

	if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil {
		responseMsg := api.NewMessage(chat.ID, i18n.Get("This command must be used as a reply to a message", r.s.GetLanguage(ctx, chat.ID, user)))
		responseMsg.ReplyParameters.MessageID = msg.MessageID
		responseMsg.ReplyParameters.ChatID = chat.ID
		responseMsg.MessageThreadID = msg.MessageThreadID
		_, _ = r.s.GetBot().Send(responseMsg)
		return nil
	}

	member, err := r.s.GetBot().GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
			UserID: user.ID,
		},
	})
	if err != nil {
		entry.WithError(err).Error("Failed to get chat member")
		return errors.Wrap(err, "failed to get chat member")
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	if permissions.IsPrivilegedModerator(&member) {
		_, err := r.spamControl.ProcessBannedMessage(ctx, msg.ReplyToMessage, chat, language)
		if err != nil {
			entry.WithError(err).Error("Failed to process spam message")
			return errors.Wrap(err, "failed to process spam message")
		}
		if err := r.s.DeleteMember(ctx, chat.ID, msg.ReplyToMessage.From.ID); err != nil {
			entry.WithError(err).Error("Failed to delete member")
		}
		_ = bot.DeleteChatMessage(ctx, r.s.GetBot(), chat.ID, msg.MessageID)
		return nil
	}

	if settings != nil && !settings.CommunityVotingEnabled {
		responseMsg := api.NewMessage(chat.ID, i18n.Get("Community voting is disabled", language))
		responseMsg.ReplyParameters.MessageID = msg.MessageID
		responseMsg.ReplyParameters.ChatID = chat.ID
		responseMsg.MessageThreadID = msg.MessageThreadID
		_, _ = r.s.GetBot().Send(responseMsg)
		return nil
	}

	if _, err := r.spamControl.ProcessSpamMessage(ctx, msg.ReplyToMessage, chat, language); err != nil {
		entry.WithError(err).Error("Failed to process spam message")
		return errors.Wrap(err, "failed to process spam message")
	}

	return nil
}
