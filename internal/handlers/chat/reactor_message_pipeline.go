package handlers

import (
	"context"
	"fmt"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

func (r *Reactor) handleMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	entry := r.getLogEntry().WithFields(log.Fields{
		"chat_id": chat.ID,
		"user_id": user.ID,
	})

	result := &MessageProcessingResult{
		Message: msg,
		Stage:   StageInit,
	}
	r.storeLastResult(int64(msg.MessageID), result)

	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("Failed to check membership")
		return fmt.Errorf("failed to check membership: %w", err)
	}

	result.Stage = StageMembershipCheck
	if isMember {
		result.Skipped = true
		result.SkipReason = "User is already a member"
		return nil
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	result.Stage = StageBanCheck

	isBanned, err := r.banService.CheckBan(ctx, user.ID)
	if err != nil {
		return errors.Wrap(err, "failed to check ban")
	}
	if isBanned {
		result.Skipped = true
		result.SkipReason = "User is banned"
		if r.config.SpamControl.DebugUserID != 0 {
			debugMsg := tool.ExecTemplate(`Banned user: {{ .user_name }} ({{ .user_id }})`, map[string]any{
				"user_name": bot.GetUN(user),
				"user_id":   user.ID,
			})
			_, _ = r.s.GetBot().Send(api.NewMessage(r.config.SpamControl.DebugUserID, debugMsg))
		}
		processingResult, err := r.spamControl.ProcessBannedMessage(ctx, msg, chat, language)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to process banned message")
			result.Actions.Error = err.Error()
		} else if processingResult != nil {
			result.Actions.MessageDeleted = processingResult.MessageDeleted
			result.Actions.UserBanned = processingResult.UserBanned
			result.Actions.Error = processingResult.Error
			if !processingResult.MessageDeleted || !processingResult.UserBanned {
				result.SkipReason += fmt.Sprintf(" (Actions: message_deleted=%v, user_banned=%v",
					processingResult.MessageDeleted, processingResult.UserBanned)
				if processingResult.Error != "" {
					result.SkipReason += fmt.Sprintf(", error=%s", processingResult.Error)
				}
				result.SkipReason += ")"
			}
		}
		return nil
	}

	if settings != nil && !settings.LLMFirstMessageEnabled {
		result.Stage = StageSpamCheck
		result.Skipped = true
		result.SkipReason = "LLM first message check disabled"
		return r.insertMemberIfPresent(ctx, chat, user, entry)
	}

	result.Stage = StageContentCheck
	content := bot.ExtractContentFromMessage(msg)
	if content == "" {
		result.Skipped = true
		result.SkipReason = "Empty message content"
		entry.WithField("message", msg).Warn("empty message content")
		return nil
	}

	result.Stage = StageSpamCheck
	isSpam, err := r.checkMessageForSpam(ctx, chat.ID, content, user)
	if err != nil {
		return err
	}
	result.IsSpam = isSpam

	if isSpam != nil {
		if *isSpam {
			var processingResult *moderation.ProcessingResult
			var processErr error
			if settings != nil && !settings.CommunityVotingEnabled {
				processingResult, processErr = r.spamControl.ProcessBannedMessage(ctx, msg, chat, language)
			} else {
				processingResult, processErr = r.spamControl.ProcessSpamMessage(ctx, msg, chat, language)
			}
			if processErr != nil {
				entry.WithField("error", processErr.Error()).Error("failed to process spam message")
				result.Actions.Error = processErr.Error()
			} else if processingResult != nil {
				result.Actions.MessageDeleted = processingResult.MessageDeleted
				result.Actions.UserBanned = processingResult.UserBanned
				result.Actions.Error = processingResult.Error
				if !processingResult.MessageDeleted || !processingResult.UserBanned {
					result.SkipReason = fmt.Sprintf("Spam detected (Actions: message_deleted=%v, user_banned=%v",
						processingResult.MessageDeleted, processingResult.UserBanned)
					if processingResult.Error != "" {
						result.SkipReason += fmt.Sprintf(", error=%s", processingResult.Error)
					}
					result.SkipReason += ")"
				}
			}
			return nil
		}

		if err := r.insertMemberIfPresent(ctx, chat, user, entry); err != nil {
			return err
		}
	}

	return nil
}

func (r *Reactor) checkMessageForSpam(ctx context.Context, chatID int64, content string, user *api.User) (*bool, error) {
	words := strings.Fields(content)
	for i, word := range words {
		if hasCyrillics(word) {
			words[i] = normalizeCyrillics(word)
		}
	}
	contentAltered := strings.Join(words, " ")

	examples := r.loadSpamExamples(ctx, chatID)
	isSpam, err := r.spamDetector.IsSpam(ctx, contentAltered, examples)
	if r.config.SpamControl.DebugUserID != 0 {
		debugMsg := tool.ExecTemplate(`
{{- .content }}

---
Is Spam result: {{ .isSpam -}}
`, map[string]any{
			"content": content,
			"isSpam":  isSpam,
		})
		_, _ = r.s.GetBot().Send(api.NewMessage(r.config.SpamControl.DebugUserID, debugMsg))
	}

	return isSpam, err
}

func (r *Reactor) loadSpamExamples(ctx context.Context, chatID int64) []string {
	examples, err := r.store.ListChatSpamExamples(ctx, chatID, maxSpamExamples, 0)
	if err != nil {
		r.getLogEntry().WithField("error", err.Error()).Error("failed to load spam examples")
		return nil
	}
	texts := make([]string, 0, len(examples))
	for _, example := range examples {
		text := strings.TrimSpace(example.Text)
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return texts
}

func (r *Reactor) insertMemberIfPresent(ctx context.Context, chat *api.Chat, user *api.User, entry *log.Entry) error {
	chatMember, err := r.s.GetBot().GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
			UserID: user.ID,
		},
	})
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get chat member")
		return err
	}

	if !(chatMember.HasLeft() || chatMember.WasKicked()) {
		entry.WithFields(log.Fields{
			"user_id": user.ID,
			"chat_id": chat.ID,
		}).Info("Adding user as member after spam check")
		if insertErr := r.s.InsertMember(ctx, chat.ID, user.ID); insertErr != nil {
			entry.WithField("error", insertErr.Error()).Error("failed to insert member")
		}
	} else {
		entry.WithFields(log.Fields{
			"user_id": user.ID,
			"chat_id": chat.ID,
		}).Info("User has left the chat, skipping member insertion")
	}
	return nil
}
