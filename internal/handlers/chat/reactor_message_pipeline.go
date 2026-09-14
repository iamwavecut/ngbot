package handlers

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const (
	messageSkipReasonAlreadyMember       = "User is already a member"
	messageSkipReasonGraduatedProbation  = "Author has active message trust"
	messageSkipReasonChatAdministrator   = "User is chat administrator"
	messageSkipReasonLinkedChannelSender = "Linked channel sender"
	messageSkipReasonChatSender          = "Chat sender"
	messageSkipReasonAnonymousSender     = "Unsupported anonymous sender"
	messageSkipReasonNoModerationRights  = "Bot has no moderation rights"
	messageSkipReasonLLMUnavailable      = "LLM classification unavailable"
	messageSkipReasonAlreadyChecked      = "Message already checked"
	logFieldTrustPhase                   = "trust_phase"
)

func (r *Reactor) handleMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	return r.handleMessageChallenge(ctx, msg, chat, user, settings, false, false)
}

func (r *Reactor) handleMessageChallenge(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings, recheck, routed bool) error {
	author, identified := bot.MessageAuthor(msg)
	entry := r.getLogEntry().WithFields(log.Fields{logFieldChatID: chat.ID, "author_kind": author.Kind, "author_id": author.ID})
	result := &MessageProcessingResult{Message: msg, Stage: StageInit}
	r.storeLastResult(chat.ID, msg.MessageID, result)
	defer func() {
		entry.WithFields(log.Fields{"stage": result.Stage, "skipped": result.Skipped, "reason": result.SkipReason, logFieldMessageID: msg.MessageID}).Debug("message moderation decision")
	}()

	available, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)
	}
	if !available {
		result.Skipped, result.SkipReason = true, messageSkipReasonNoModerationRights
		return nil
	}
	skipReason, trusted, err := r.trustedSenderChat(ctx, msg, chat, entry)
	if err != nil {
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureTelegram, "sender_chat_classification_failed", err)
	}
	if trusted {
		result.Skipped, result.SkipReason = true, skipReason
		return r.rememberMessageContext(ctx, msg, chat, settings)
	}
	if !identified {
		result.Skipped, result.SkipReason = true, messageSkipReasonAnonymousSender
		return nil
	}
	if author.Kind == db.MessageAuthorUser {
		user = msg.From
		result.Stage = StageOverrideCheck
		allowlisted, overrideErr := r.store.IsChatNotSpammer(ctx, chat.ID, author.ID, user.UserName)
		if overrideErr != nil {
			entry.WithError(overrideErr).Error("failed to check manual not-spammer override; continuing moderation")
		} else if allowlisted {
			result.Skipped, result.SkipReason = true, "User is manually marked as not spammer"
			if err := r.rememberAuthorIfPossible(ctx, chat, user, entry); err != nil {
				entry.WithError(err).Warn("allowlisted user membership bookkeeping failed")
			}
			return r.rememberMessageContext(ctx, msg, chat, settings)
		}
		result.Stage = StageBanCheck
		banned := r.banService != nil && r.banService.IsKnownBanned(author.ID)
		if !banned && r.banService != nil && !banlistWasPrechecked(ctx) {
			banned, err = r.banService.CheckBan(ctx, author.ID)
			if err != nil {
				return errors.Wrap(err, "failed to check ban")
			}
		}
		if banned {
			return r.enforceBanlistedMessage(ctx, msg, chat, user, result, entry)
		}
	}
	if settings != nil && !settings.LLMFirstMessageEnabled {
		result.Skipped, result.SkipReason = true, "Message moderation disabled"
		return nil
	}
	if err := r.rememberMessageContext(ctx, msg, chat, settings); err != nil {
		return err
	}
	now := r.currentTime()
	trust, err := r.store.EnsureMessageTrust(ctx, chat.ID, author)
	if err != nil {
		return fmt.Errorf("get message trust: %w", err)
	}
	if !recheck && trust.Trusted(now) {
		result.Skipped, result.SkipReason = true, messageSkipReasonGraduatedProbation
		r.recordMessageTrustStat(ctx, chat.ID, "author_trust_skipped", entry)
		entry.Debug("skipping trusted author message")
		return nil
	}
	if !recheck {
		checked, err := r.store.IsCheckedAuthorMessage(ctx, chat.ID, author, msg.MessageID)
		if err != nil {
			return fmt.Errorf("check message binding: %w", err)
		}
		if checked {
			result.Skipped, result.SkipReason = true, messageSkipReasonAlreadyChecked
			return nil
		}
	}
	if author.Kind == db.MessageAuthorUser && r.isChatAdministrator(ctx, chat.ID, author.ID, entry) {
		result.Skipped, result.SkipReason = true, messageSkipReasonChatAdministrator
		return nil
	}
	result.Stage = StageContentCheck
	if bot.ExtractTextFromMessage(msg) == "" {
		result.Skipped, result.SkipReason = true, "Empty message content"
		return nil
	}
	conversation, err := r.messageConversation(ctx, msg, chat)
	if err != nil {
		return fmt.Errorf("load message context: %w", err)
	}
	phase := "initial"
	if trust.TrustedUntil.Valid {
		phase = "renewal"
	}
	if trust.Suspended {
		phase = "pending_case"
	}
	if recheck {
		phase = "edit"
	}
	entry = entry.WithFields(log.Fields{logFieldTrustPhase: phase, logFieldMessageID: msg.MessageID})
	result.Stage = StageSpamCheck
	r.recordMessageTrustStat(ctx, chat.ID, "author_check_"+phase, entry)
	isSpam, err := r.checkMessageForSpam(ctx, settings, bot.ExtractContentFromMessage(msg), conversation...)
	if err == nil && isSpam == nil {
		err = llm.NewFailure(llm.FailureMalformedOutput, fmt.Errorf("classification returned no decision"))
	}
	if err != nil {
		result.Skipped, result.SkipReason = true, messageSkipReasonLLMUnavailable
		entry.WithFields(classificationFailureLogFields(err, "message", "durable_retry")).Warn("message LLM classification scheduled for durable retry")
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureLLM, string(llm.FailureKindOf(err)), err)
	}
	result.IsSpam = isSpam
	entry.WithField("is_spam", *isSpam).Debug("message author classification completed")
	if *isSpam {
		language := r.s.GetLanguage(ctx, chat.ID, user)
		processed, err := r.processDetectedSpam(ctx, msg, chat, language, settings)
		if processed != nil {
			result.Actions.MessageDeleted = processed.MessageDeleted
			result.Actions.UserBanned = processed.UserBanned
			result.Actions.Error = processed.Error
			if processed.MessageDeleted {
				err = stderrors.Join(err, r.store.DeleteMessageContext(ctx, chat.ID, msg.MessageID))
			}
		}
		if err != nil {
			result.Actions.Error = err.Error()
		}
		return err
	}
	updated, _, err := r.store.RecordSafeAuthorMessage(ctx, chat.ID, author, msg.MessageID, now, r.safeMessagesRequired(), r.authorTrustDuration(), !recheck && !routed && !messageIsCommand(msg) && !messageMentionsCurrentBot(msg, r.bot.Self))
	if err != nil {
		return fmt.Errorf("record challenged message and trust: %w", err)
	}
	if recheck {
		return nil
	}
	if updated.Trusted(now) && !trust.Trusted(now) {
		r.recordMessageTrustStat(ctx, chat.ID, "author_trust_granted", entry)
		entry.WithField("trusted_until", updated.TrustedUntil.Time).Info("message author trust granted")
		if author.Kind == db.MessageAuthorUser {
			if err := r.rememberAuthorIfPossible(ctx, chat, user, entry); err != nil {
				entry.WithError(err).Warn("membership bookkeeping failed after author trust was persisted")
			}
		}
	}
	return nil
}

func (r *Reactor) recordMessageTrustStat(ctx context.Context, chatID int64, metric string, entry *log.Entry) {
	if err := handlersbase.IncrementDailyStatAt(ctx, r.stats, chatID, metric, r.currentTime()); err != nil {
		entry.WithError(err).Warn("failed to increment author trust stat")
	}
}

func classificationFailureLogFields(err error, path string, fallback string) log.Fields {
	return log.Fields{logFieldError: "classification_failed", "classification_path": path, "fallback": fallback, "llm_outcome": string(llm.FailureKindOf(err))}
}

func (r *Reactor) enforceBanlistedMessage(
	ctx context.Context,
	msg *api.Message,
	chat *api.Chat,
	user *api.User,
	result *MessageProcessingResult,
	entry *log.Entry,
) error {
	result.Stage = StageBanCheck
	result.Skipped = true
	result.SkipReason = "User is banned"

	outcome := enforceBanlistedMessage(ctx, r.bot, r.store, r.banService, msg, chat, user)
	if outcome.userBanned {
		outcome.err = stderrors.Join(outcome.err, r.store.ResetMessageTrust(ctx, chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: user.ID}))
	}
	if outcome.messageDeleted {
		outcome.err = stderrors.Join(outcome.err, r.store.DeleteMessageContext(ctx, chat.ID, msg.MessageID))
	}
	result.Actions.MessageDeleted = outcome.messageDeleted
	result.Actions.UserBanned = outcome.userBanned
	if !outcome.moderationAvailable {
		result.SkipReason = messageSkipReasonNoModerationRights
	}
	if outcome.err != nil {
		result.Actions.Error = outcome.err.Error()
		entry.WithField(logFieldError, outcome.err.Error()).Error("failed to enforce terminal banlist action")
		return outcome.err
	}
	return nil
}

func (r *Reactor) moderationAvailable(ctx context.Context, chatID int64) (bool, error) {
	if r.banService == nil {
		return true, nil
	}
	return r.banService.ModerationAvailable(ctx, chatID)
}

func (r *Reactor) HandleExhaustedUpdateFailure(
	ctx context.Context,
	update *api.Update,
	chat *api.Chat,
	user *api.User,
	failure bot.UpdateFailure,
) error {
	if failure.Source != bot.UpdateFailureLLM || update == nil || chat == nil || user == nil || r.banService == nil {
		return nil
	}
	message := update.Message
	if message == nil {
		message = update.EditedMessage
	}
	if message == nil || message.SenderChat != nil {
		return nil
	}
	available, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		return fmt.Errorf("inspect moderation rights for LLM degradation: %w", err)
	}
	if !available {
		return nil
	}
	if err := r.banService.MuteUser(ctx, chat.ID, user.ID, time.Time{}); err != nil {
		return fmt.Errorf("quarantine user after LLM exhaustion: %w", err)
	}
	return nil
}

func (r *Reactor) markModerationUnavailableOnPrivilege(chatID int64, err error) {
	if r.banService == nil || !moderation.IsTelegramPrivilegeError(err) {
		return
	}
	r.banService.MarkModerationUnavailable(chatID)
}

func (r *Reactor) trustedSenderChat(ctx context.Context, msg *api.Message, chat *api.Chat, entry *log.Entry) (string, bool, error) {
	if msg == nil || chat == nil || msg.SenderChat == nil {
		return "", false, nil
	}
	return r.trustedSenderChatIdentity(ctx, msg.SenderChat, chat, msg.IsAutomaticForward, entry)
}

func (r *Reactor) trustedSenderChatIdentity(ctx context.Context, senderChat *api.Chat, chat *api.Chat, automaticForward bool, entry *log.Entry) (string, bool, error) {
	if senderChat == nil || chat == nil {
		return "", false, nil
	}
	if senderChat.ID == chat.ID {
		return messageSkipReasonChatSender, true, nil
	}
	if !senderChat.IsChannel() {
		return "", false, nil
	}
	if automaticForward {
		return messageSkipReasonLinkedChannelSender, true, nil
	}

	info, err := r.messageChatInfo(ctx, chat)
	if err != nil {
		entry.WithError(err).Warn("failed to verify linked channel sender")
		return "", false, err
	}
	return messageSkipReasonLinkedChannelSender, info.linkedChatID == senderChat.ID, nil
}

func (r *Reactor) isChatAdministrator(ctx context.Context, chatID int64, userID int64, entry *log.Entry) bool {
	member, err := bot.GetChatMember(ctx, r.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{ChatID: chatID},
			UserID:     userID,
		},
	})
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Warn("failed to verify message sender administrator status")
		return false
	}
	return member.IsCreator() || member.IsAdministrator()
}

func (r *Reactor) processDetectedSpam(ctx context.Context, msg *api.Message, chat *api.Chat, language string, settings *db.Settings) (*moderation.ProcessingResult, error) {
	if settings != nil && !settings.CommunityVotingEnabled {
		return r.processBanned(ctx, msg, chat, language)
	}
	return r.processSpam(ctx, msg, chat, language)
}

func (r *Reactor) checkMessageForSpam(ctx context.Context, settings *db.Settings, content string, conversation ...moderation.ConversationMessage) (*bool, error) {
	words := strings.Fields(content)
	for i, word := range words {
		if hasCyrillics(word) {
			words[i] = normalizeCyrillics(word)
		}
	}
	contentAltered := strings.Join(words, " ")

	classificationContext := r.loadClassificationContext(ctx, settings)
	classificationContext.Conversation = conversation
	if r.spamDetector == nil {
		return nil, llm.NewFailure(llm.FailureProvider, fmt.Errorf("spam detector unavailable"))
	}
	isSpam, err := r.spamDetector.IsSpam(ctx, contentAltered, classificationContext)
	if err == nil && settings != nil {
		if statErr := handlersbase.IncrementDailyStat(ctx, r.stats, settings.ID, handlersbase.StatLLMChecked); statErr != nil {
			r.getLogEntry().WithField(logFieldError, statErr.Error()).Warn("failed to increment LLM checked stat")
		}
	}
	return isSpam, err
}

func (r *Reactor) checkReportedMessageForSpam(ctx context.Context, settings *db.Settings, content string, conversation ...moderation.ConversationMessage) (*bool, error) {
	if r.spamDetector == nil {
		return nil, nil
	}
	words := strings.Fields(content)
	for i, word := range words {
		if hasCyrillics(word) {
			words[i] = normalizeCyrillics(word)
		}
	}
	contentAltered := strings.Join(words, " ")

	classificationContext := r.loadClassificationContext(ctx, settings)
	classificationContext.Conversation = conversation
	isSpam, err := r.spamDetector.IsReportedSpam(ctx, contentAltered, classificationContext)
	if err == nil && settings != nil {
		if statErr := handlersbase.IncrementDailyStat(ctx, r.stats, settings.ID, handlersbase.StatLLMChecked); statErr != nil {
			r.getLogEntry().WithField(logFieldError, statErr.Error()).Warn("failed to increment reported LLM checked stat")
		}
	}
	return isSpam, err
}

func (r *Reactor) loadClassificationContext(ctx context.Context, settings *db.Settings) moderation.ClassificationContext {
	classificationContext := moderation.ClassificationContext{Profile: db.LLMModerationProfileGeneral}
	if settings == nil {
		return classificationContext
	}
	classificationContext.Profile = settings.LLMModerationProfile
	if r.store == nil {
		return classificationContext
	}
	for _, classification := range []int{db.SpamClassificationAllowed, db.SpamClassificationSpam} {
		examples, err := r.store.ListChatSpamExamples(ctx, settings.ID, classification, maxSpamExamples, 0)
		if err != nil {
			r.getLogEntry().WithField(logFieldError, err.Error()).WithField("classification", classification).Error("failed to load moderation examples")
			continue
		}
		for _, example := range examples {
			text := strings.TrimSpace(example.Text)
			if text == "" {
				continue
			}
			classificationContext.Examples = append(classificationContext.Examples, moderation.ClassificationExample{
				Message:        text,
				Classification: classification,
			})
		}
	}
	return classificationContext
}

func (r *Reactor) rememberAuthorIfPossible(ctx context.Context, chat *api.Chat, user *api.User, entry *log.Entry) error {
	chatMember, err := bot.GetChatMember(ctx, r.bot, api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
			UserID: user.ID,
		},
	})
	if err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "CHAT_ADMIN_REQUIRED") {
			entry.WithField(logFieldError, err.Error()).Warn("cannot inspect chat member without administrator rights")
			return nil
		}
		entry.WithField(logFieldError, err.Error()).Error("failed to get chat member")
		return err
	}

	if chatMember.WasKicked() {
		if deleteErr := r.store.DeleteChatKnownNonMember(ctx, chat.ID, user.ID); deleteErr != nil {
			entry.WithField(logFieldError, deleteErr.Error()).Error("failed to delete kicked known non-member")
		}
		entry.WithFields(log.Fields{
			logFieldUserID: user.ID,
			logFieldChatID: chat.ID,
		}).Info("User was kicked from the chat, skipping author memory update")
		return nil
	}

	if !isCurrentChatMember(chatMember) {
		entry.WithFields(log.Fields{
			logFieldUserID: user.ID,
			logFieldChatID: chat.ID,
		}).Info("Remembering user as known non-member after spam check")
		if upsertErr := r.store.UpsertChatKnownNonMember(ctx, &db.ChatKnownNonMember{
			ChatID: chat.ID,
			UserID: user.ID,
		}); upsertErr != nil {
			entry.WithField(logFieldError, upsertErr.Error()).Error("failed to upsert known non-member")
			return upsertErr
		}
		return nil
	}

	entry.WithFields(log.Fields{
		logFieldUserID: user.ID,
		logFieldChatID: chat.ID,
	}).Info("Adding user as member after spam check")
	if insertErr := r.s.InsertMember(ctx, chat.ID, user.ID); insertErr != nil {
		entry.WithField(logFieldError, insertErr.Error()).Error("failed to insert member")
		return insertErr
	} else if deleteErr := r.store.DeleteChatKnownNonMember(ctx, chat.ID, user.ID); deleteErr != nil {
		entry.WithField(logFieldError, deleteErr.Error()).Error("failed to delete known non-member after member insert")
	}

	return nil
}
