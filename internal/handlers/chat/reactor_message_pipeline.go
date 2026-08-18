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
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type firstMessageExternalQuoteHeuristic struct {
	Triggered        bool
	HasExternalReply bool
	HasQuote         bool
	HasForwardOrigin bool
	HasViaBot        bool
	OriginType       string
	OriginChatID     int64
	ViaBotID         int64
}

const (
	messageSkipReasonAlreadyMember       = "User is already a member"
	messageSkipReasonGraduatedProbation  = "User completed message probation"
	messageSkipReasonChatAdministrator   = "User is chat administrator"
	messageSkipReasonLinkedChannelSender = "Linked channel sender"
	messageSkipReasonChatSender          = "Chat sender"
	messageSkipReasonAnonymousSender     = "Unsupported anonymous sender"
	messageSkipReasonNoModerationRights  = "Bot has no moderation rights"
	messageSkipReasonExternalQuote       = "First-message external quote heuristic"
	messageSkipReasonLLMUnavailable      = "LLM classification unavailable"
	logFieldProbationPhase               = "probation_phase"
)

func (r *Reactor) handleMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	return r.handleMessageChallenge(ctx, msg, chat, user, settings, false, false)
}

func (r *Reactor) handleMessageChallenge(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings, recheck, routed bool) error {
	var userID int64
	if user != nil {
		userID = user.ID
	}

	entry := r.getLogEntry().WithFields(log.Fields{
		logFieldChatID: chat.ID,
		logFieldUserID: userID,
	})

	result := &MessageProcessingResult{
		Message: msg,
		Stage:   StageInit,
	}
	r.storeLastResult(chat.ID, msg.MessageID, result)

	skipReason, trusted, err := r.trustedSenderChat(ctx, msg, chat, entry)
	if err != nil {
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureTelegram, "sender_chat_classification_failed", err)
	}
	if trusted {
		result.Stage = StageSpamCheck
		result.Skipped = true
		result.SkipReason = skipReason
		entry.WithField("sender_chat_id", msg.SenderChat.ID).Debug("Skipping trusted sender chat from spam pipeline")
		return nil
	}
	if msg.SenderChat != nil {
		return r.handleSenderChatContent(ctx, msg, chat, settings, result, entry)
	}

	if user == nil {
		result.Stage = StageSpamCheck
		result.Skipped = true
		result.SkipReason = messageSkipReasonAnonymousSender
		if msg.SenderChat != nil {
			entry = entry.WithField("sender_chat_id", msg.SenderChat.ID)
		}
		entry.Warn("ignoring unsupported anonymous sender")
		return nil
	}
	moderationAvailable, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Warn("failed to inspect moderation rights; scheduling durable retry")
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)
	}
	if !moderationAvailable {
		result.Stage = StageSpamCheck
		result.Skipped = true
		result.SkipReason = messageSkipReasonNoModerationRights
		return nil
	}
	result.Stage = StageOverrideCheck
	isNotSpammer, err := r.store.IsChatNotSpammer(ctx, chat.ID, user.ID, user.UserName)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to check manual not-spammer override; continuing moderation")
	} else if isNotSpammer {
		result.Skipped = true
		result.SkipReason = "User is manually marked as not spammer"
		if recheck || routed {
			return nil
		}
		_, err = r.rememberAuthorIfPossible(ctx, chat, user, entry)
		return err
	}
	if r.banService != nil && r.banService.IsKnownBanned(user.ID) {
		return r.enforceBanlistedMessage(ctx, msg, chat, user, result, entry)
	}

	observedAt := r.currentTime()
	probation, err := r.store.MessageProbation(ctx, chat.ID, user.ID)
	if err != nil {
		return fmt.Errorf("get message probation: %w", err)
	}
	result.Stage = StageMembershipCheck
	if !recheck && probation == nil {
		isMember, memberErr := r.s.IsMember(ctx, chat.ID, user.ID)
		if memberErr != nil {
			entry.WithField(logFieldError, memberErr.Error()).Error("Failed to check membership")
			return fmt.Errorf("failed to check membership: %w", memberErr)
		}
		if isMember {
			result.Skipped = true
			result.SkipReason = messageSkipReasonAlreadyMember
			return nil
		}
	}

	result.Stage = StageBanCheck
	isBanned := false
	if r.banService != nil && !banlistWasPrechecked(ctx) {
		isBanned, err = r.banService.CheckBan(ctx, user.ID)
	}
	if err != nil {
		return errors.Wrap(err, "failed to check ban")
	}
	if isBanned {
		return r.enforceBanlistedMessage(ctx, msg, chat, user, result, entry)
	}

	if r.isChatAdministrator(ctx, chat.ID, user.ID, entry) {
		result.Skipped = true
		result.SkipReason = messageSkipReasonChatAdministrator
		return nil
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)

	if !recheck {
		if settings != nil && !settings.LLMFirstMessageEnabled {
			result.Stage = StageSpamCheck
			result.Skipped = true
			result.SkipReason = "Message probation disabled"
			if probation != nil {
				return nil
			}
			_, err = r.rememberAuthorIfPossible(ctx, chat, user, entry)
			return err
		}
		if probation != nil && probation.GraduatedAt.Valid {
			result.Skipped = true
			result.SkipReason = messageSkipReasonGraduatedProbation
			return nil
		}
		if probation == nil {
			probation, err = r.startMessageProbation(ctx, chat.ID, user.ID, observedAt, entry)
			if err != nil {
				return err
			}
		}
	}

	result.Stage = StageContentCheck
	if bot.ExtractTextFromMessage(msg) == "" {
		result.Skipped = true
		result.SkipReason = "Empty message content"
		entry.WithField(logFieldMessageID, msg.MessageID).Debug("empty message content")
		return nil
	}
	content := bot.ExtractContentFromMessage(msg)
	if content == "" {
		result.Skipped = true
		result.SkipReason = "Empty message content"
		entry.WithField(logFieldMessageID, msg.MessageID).Debug("empty message content")
		return nil
	}

	result.Stage = StageSpamCheck
	heuristic := detectFirstMessageExternalQuoteHeuristic(msg)
	if heuristic.Triggered {
		result.SkipReason = messageSkipReasonExternalQuote
		result.IsSpam = tool.Ptr(true)
		if err := handlersbase.IncrementDailyStat(ctx, r.stats, chat.ID, handlersbase.StatHeuristicSpam); err != nil {
			entry.WithField(logFieldError, err.Error()).Warn("failed to increment heuristic spam stat")
		}

		processingResult, processErr := r.processDetectedSpam(ctx, msg, chat, language, settings)
		if processErr != nil {
			entry.WithFields(log.Fields{
				logFieldError:        processErr.Error(),
				"has_external_reply": heuristic.HasExternalReply,
				"has_quote":          heuristic.HasQuote,
				"has_forward_origin": heuristic.HasForwardOrigin,
				"has_via_bot":        heuristic.HasViaBot,
				"origin_type":        heuristic.OriginType,
				"origin_chat_id":     heuristic.OriginChatID,
				"via_bot_id":         heuristic.ViaBotID,
			}).Error("failed to process spam message from external quote heuristic")
			result.Actions.Error = processErr.Error()
			return processErr
		} else if processingResult != nil {
			result.Actions.MessageDeleted = processingResult.MessageDeleted
			result.Actions.UserBanned = processingResult.UserBanned
			result.Actions.Error = processingResult.Error
			if !processingResult.MessageDeleted || !processingResult.UserBanned {
				result.SkipReason = fmt.Sprintf("First-message external quote heuristic (Actions: message_deleted=%v, user_banned=%v",
					processingResult.MessageDeleted, processingResult.UserBanned)
				if processingResult.Error != "" {
					result.SkipReason += fmt.Sprintf(", error=%s", processingResult.Error)
				}
				result.SkipReason += ")"
			}
		}

		entry.WithFields(log.Fields{
			"has_external_reply":   heuristic.HasExternalReply,
			"has_quote":            heuristic.HasQuote,
			"has_forward_origin":   heuristic.HasForwardOrigin,
			"has_via_bot":          heuristic.HasViaBot,
			"origin_type":          heuristic.OriginType,
			"origin_chat_id":       heuristic.OriginChatID,
			"via_bot_id":           heuristic.ViaBotID,
			logFieldProbationPhase: messageProbationPhase(probation, observedAt),
		}).Info("Detected spam with first-message external quote heuristic")
		return nil
	}

	isSpam, err := r.checkMessageForSpam(ctx, settings, content)
	if err != nil {
		result.Skipped = true
		result.SkipReason = messageSkipReasonLLMUnavailable
		entry.WithFields(classificationFailureLogFields(err, "message", "durable_retry")).Warn("message LLM classification scheduled for durable retry")
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureLLM, string(llm.FailureKindOf(err)), err)
	}
	result.IsSpam = isSpam

	if isSpam != nil {
		if *isSpam {
			entry.WithFields(log.Fields{
				logFieldMessageID:      msg.MessageID,
				"edited":               recheck,
				logFieldProbationPhase: messageProbationPhase(probation, observedAt),
			}).Info("message probation detected spam")
			processingResult, processErr := r.processDetectedSpam(ctx, msg, chat, language, settings)
			if processErr != nil {
				entry.WithField(logFieldError, processErr.Error()).Error("failed to process spam message")
				result.Actions.Error = processErr.Error()
				return processErr
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

		if recheck {
			return nil
		}
		inserted, err := r.store.RecordChallengedMessage(ctx, chat.ID, user.ID, msg.MessageID)
		if err != nil {
			return fmt.Errorf("record challenged message: %w", err)
		}
		entry.WithFields(log.Fields{
			logFieldMessageID:      msg.MessageID,
			"inserted":             inserted,
			logFieldProbationPhase: messageProbationPhase(probation, observedAt),
		}).Debug("message probation checked safe content")
		if routed || !inserted || probation == nil || observedAt.Before(probation.EligibleAt) {
			return nil
		}
		remembered, rememberErr := r.rememberAuthorIfPossible(ctx, chat, user, entry)
		if rememberErr != nil {
			return rememberErr
		}
		if !remembered {
			return nil
		}
		if err := r.store.MarkMessageProbationGraduated(ctx, chat.ID, user.ID, observedAt); err != nil {
			return fmt.Errorf("graduate message probation: %w", err)
		}
		entry.WithFields(log.Fields{
			logFieldMessageID: msg.MessageID,
			"eligible_at":     probation.EligibleAt,
			"graduated_at":    observedAt,
		}).Info("message probation graduated")
	}

	return nil
}

func (r *Reactor) handleSenderChatContent(ctx context.Context, msg *api.Message, chat *api.Chat, settings *db.Settings, result *MessageProcessingResult, entry *log.Entry) error {
	available, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		result.Skipped = true
		result.SkipReason = messageSkipReasonNoModerationRights
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)
	}
	if !available {
		result.Skipped = true
		result.SkipReason = messageSkipReasonNoModerationRights
		return nil
	}
	content := bot.ExtractContentFromMessage(msg)
	if content == "" || r.spamDetector == nil {
		result.Skipped = true
		result.SkipReason = messageSkipReasonAnonymousSender
		return nil
	}
	result.Stage = StageSpamCheck
	isSpam, err := r.checkMessageForSpam(ctx, settings, content)
	if err != nil {
		result.Skipped = true
		result.SkipReason = messageSkipReasonLLMUnavailable
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureLLM, string(llm.FailureKindOf(err)), err)
	}
	result.IsSpam = isSpam
	if isSpam == nil || !*isSpam {
		return nil
	}
	var actionErr error
	if err := bot.DeleteChatMessage(ctx, r.bot, chat.ID, msg.MessageID); err != nil && !isTelegramMessageAlreadyDeleted(err) {
		actionErr = stderrors.Join(actionErr, fmt.Errorf("delete sender chat message: %w", err))
	} else {
		result.Actions.MessageDeleted = true
	}
	if _, err := r.bot.RequestWithContext(ctx, api.BanChatSenderChatConfig{
		ChatConfig:   api.ChatConfig{ChatID: chat.ID},
		SenderChatID: msg.SenderChat.ID,
	}); err != nil {
		r.markModerationUnavailableOnPrivilege(chat.ID, err)
		actionErr = stderrors.Join(actionErr, fmt.Errorf("ban sender chat: %w", err))
	} else {
		result.Actions.UserBanned = true
	}
	if actionErr != nil {
		result.Actions.Error = actionErr.Error()
		return actionErr
	}
	entry.WithField("sender_chat_id", msg.SenderChat.ID).Info("moderated untrusted sender chat")
	return nil
}

func classificationFailureLogFields(err error, path string, fallback string) log.Fields {
	return log.Fields{
		logFieldError:         "classification_failed",
		"classification_path": path,
		"fallback":            fallback,
		"llm_outcome":         string(llm.FailureKindOf(err)),
	}
}

func (r *Reactor) startMessageProbation(
	ctx context.Context,
	chatID int64,
	userID int64,
	startedAt time.Time,
	entry *log.Entry,
) (*db.MessageProbation, error) {
	eligibleAt := startedAt.Add(r.messageProbationDuration())
	probation, created, err := r.store.GetOrCreateMessageProbation(ctx, chatID, userID, startedAt, eligibleAt)
	if err != nil {
		return nil, fmt.Errorf("get or create message probation: %w", err)
	}
	if created {
		entry.WithFields(log.Fields{
			"started_at":  probation.StartedAt,
			"eligible_at": probation.EligibleAt,
		}).Info("message probation started")
	}
	return probation, nil
}

func messageProbationPhase(probation *db.MessageProbation, observedAt time.Time) string {
	if probation == nil {
		return "none"
	}
	if probation.GraduatedAt.Valid {
		return "graduated"
	}
	if observedAt.Before(probation.EligibleAt) {
		return "active"
	}
	return "eligible"
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

	outcome := enforceBanlistedMessage(ctx, r.bot, r.banService, msg, chat, user)
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
	if message == nil {
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

func detectFirstMessageExternalQuoteHeuristic(msg *api.Message) firstMessageExternalQuoteHeuristic {
	result := firstMessageExternalQuoteHeuristic{}
	if msg == nil {
		return result
	}

	result.HasQuote = msg.Quote != nil
	result.HasForwardOrigin = msg.ForwardOrigin != nil
	result.HasViaBot = msg.ViaBot != nil
	if msg.ViaBot != nil {
		result.ViaBotID = msg.ViaBot.ID
	}
	if msg.ExternalReply == nil {
		return result
	}

	result.HasExternalReply = true
	result.OriginType = msg.ExternalReply.Origin.Type
	if msg.ExternalReply.Chat != nil {
		result.OriginChatID = msg.ExternalReply.Chat.ID
	}

	if msg.ExternalReply.Chat != nil && msg.ExternalReply.Chat.ID == msg.Chat.ID {
		return result
	}

	result.Triggered = true
	return result
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

	fullChat, err := bot.GetChat(ctx, r.bot, api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{ChatID: chat.ID},
	})
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Warn("failed to verify linked channel sender")
		return "", false, err
	}
	return messageSkipReasonLinkedChannelSender, fullChat.LinkedChatID == senderChat.ID, nil
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

func (r *Reactor) checkMessageForSpam(ctx context.Context, settings *db.Settings, content string) (*bool, error) {
	words := strings.Fields(content)
	for i, word := range words {
		if hasCyrillics(word) {
			words[i] = normalizeCyrillics(word)
		}
	}
	contentAltered := strings.Join(words, " ")

	classificationContext := r.loadClassificationContext(ctx, settings)
	isSpam, err := r.spamDetector.IsSpam(ctx, contentAltered, classificationContext)
	if err == nil {
		if statErr := handlersbase.IncrementDailyStat(ctx, r.stats, settings.ID, handlersbase.StatLLMChecked); statErr != nil {
			r.getLogEntry().WithField(logFieldError, statErr.Error()).Warn("failed to increment LLM checked stat")
		}
	}
	return isSpam, err
}

func (r *Reactor) checkReportedMessageForSpam(ctx context.Context, settings *db.Settings, content string) (*bool, error) {
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
	isSpam, err := r.spamDetector.IsReportedSpam(ctx, contentAltered, classificationContext)
	if err == nil {
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

func (r *Reactor) rememberAuthorIfPossible(ctx context.Context, chat *api.Chat, user *api.User, entry *log.Entry) (bool, error) {
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
			return false, nil
		}
		entry.WithField(logFieldError, err.Error()).Error("failed to get chat member")
		return false, err
	}

	if chatMember.WasKicked() {
		if deleteErr := r.store.DeleteChatKnownNonMember(ctx, chat.ID, user.ID); deleteErr != nil {
			entry.WithField(logFieldError, deleteErr.Error()).Error("failed to delete kicked known non-member")
		}
		entry.WithFields(log.Fields{
			logFieldUserID: user.ID,
			logFieldChatID: chat.ID,
		}).Info("User was kicked from the chat, skipping author memory update")
		return false, nil
	}

	if chatMember.HasLeft() {
		entry.WithFields(log.Fields{
			logFieldUserID: user.ID,
			logFieldChatID: chat.ID,
		}).Info("Remembering user as known non-member after spam check")
		if upsertErr := r.store.UpsertChatKnownNonMember(ctx, &db.ChatKnownNonMember{
			ChatID: chat.ID,
			UserID: user.ID,
		}); upsertErr != nil {
			entry.WithField(logFieldError, upsertErr.Error()).Error("failed to upsert known non-member")
			return false, upsertErr
		}
		return true, nil
	}

	entry.WithFields(log.Fields{
		logFieldUserID: user.ID,
		logFieldChatID: chat.ID,
	}).Info("Adding user as member after spam check")
	if insertErr := r.s.InsertMember(ctx, chat.ID, user.ID); insertErr != nil {
		entry.WithField(logFieldError, insertErr.Error()).Error("failed to insert member")
		return false, insertErr
	} else if deleteErr := r.store.DeleteChatKnownNonMember(ctx, chat.ID, user.ID); deleteErr != nil {
		entry.WithField(logFieldError, deleteErr.Error()).Error("failed to delete known non-member after member insert")
	}

	return true, nil
}
