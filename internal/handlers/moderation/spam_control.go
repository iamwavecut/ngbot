package handlers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	log "github.com/sirupsen/logrus"
)

type SpamControl struct {
	s          bot.Service
	bot        *api.BotAPI
	store      spamStore
	config     config.SpamControl
	banService BanService
	verbose    bool
	runtimeCtx context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	started    bool
}

type votingPolicy struct {
	Timeout             time.Duration
	MinVoters           int
	MaxVoters           int
	MinVotersPercentage float64
}

const (
	spamCaseStatusPending         = "pending"
	spamCaseStatusSpam            = db.SpamCaseStatusSpam
	spamCaseStatusFalsePositive   = db.SpamCaseStatusFalsePositive
	errChatAdminRequired          = "CHAT_ADMIN_REQUIRED"
	maxReplyQuoteRunes            = 1024
	voteBanReportMessageRetention = 10 * time.Minute
	spamWorkerInterval            = 3 * time.Second
)

var (
	ErrCommunityVotingDisabled = errors.New("community voting disabled")
	ErrSuspectCannotVote       = errors.New("suspect cannot vote on own spam case")
	ErrVoterNotEligible        = errors.New("voter is not a member of the moderated chat")
	ErrSpamCaseClosed          = errors.New("spam case is no longer pending")
)

type spamStore interface {
	CreateSpamCase(ctx context.Context, sc *db.SpamCase) (*db.SpamCase, error)
	UpdateSpamCasePresentation(ctx context.Context, sc *db.SpamCase) error
	SetSpamCasePreVoteRestricted(ctx context.Context, caseID int64, restricted bool) error
	SetSpamCaseResolveAt(ctx context.Context, caseID int64, resolveAt time.Time) (bool, error)
	GetSpamCase(ctx context.Context, id int64) (*db.SpamCase, error)
	GetPendingSpamCases(ctx context.Context) ([]*db.SpamCase, error)
	GetDueSpamCases(ctx context.Context, now time.Time) ([]*db.SpamCase, error)
	GetActiveSpamCase(ctx context.Context, chatID int64, userID int64) (*db.SpamCase, error)
	GetActiveSpamCaseByMessage(ctx context.Context, chatID int64, userID int64, messageID int) (*db.SpamCase, error)
	AddSpamCaseReportMessage(ctx context.Context, message *db.SpamCaseReportMessage) error
	GetDueSpamCaseReportMessages(ctx context.Context, before time.Time) ([]*db.SpamCaseReportMessage, error)
	DeleteSpamCaseReportMessage(ctx context.Context, caseID, chatID int64, messageID int) error
	AddVoteIfPending(ctx context.Context, vote *db.SpamVote) (notSpamVotes, spamVotes int, accepted bool, err error)
	ClaimKnownSpamCase(ctx context.Context, caseID int64, now time.Time) (*db.SpamCase, bool, error)
	ClaimSpamCaseResolution(ctx context.Context, caseID int64, requiredVoters int, timedOut bool, now time.Time) (*db.SpamCase, bool, error)
	FinalizeSpamCaseResolution(ctx context.Context, caseID int64, expectedStatus, terminalStatus, statsKey string, resolvedAt time.Time) (bool, error)
	ScheduleSpamCaseRetry(ctx context.Context, caseID int64, expectedStatus string, nextAttemptAt time.Time, lastError string) (bool, error)
	GetMembers(ctx context.Context, chatID int64) ([]int64, error)
	GetChatRecentJoiners(ctx context.Context, chatID int64) ([]*db.RecentJoiner, error)
	ProcessRecentJoiner(ctx context.Context, chatID int64, userID int64, isSpammer bool) error
	DeleteChatKnownNonMember(ctx context.Context, chatID int64, userID int64) error
}

func NewSpamControl(s bot.Service, botAPI *api.BotAPI, store spamStore, config config.SpamControl, banService BanService, verbose bool) *SpamControl {
	return &SpamControl{
		s:          s,
		bot:        botAPI,
		store:      store,
		config:     config,
		banService: banService,
		verbose:    verbose,
	}
}

func (sc *SpamControl) Start(ctx context.Context) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.started {
		return nil
	}
	sc.runtimeCtx, sc.cancel = context.WithCancel(ctx)
	sc.wg.Go(func() { sc.runDurableWorker(sc.runtimeCtx) })
	sc.started = true
	return nil
}

func (sc *SpamControl) Stop(ctx context.Context) error {
	sc.mu.Lock()
	if !sc.started {
		sc.mu.Unlock()
		return nil
	}
	sc.started = false
	cancel := sc.cancel
	sc.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (sc *SpamControl) ProcessSuspectMessage(ctx context.Context, msg *api.Message, lang string) error {
	if msg == nil {
		return nil
	}
	_, err := sc.preprocessMessage(ctx, msg, &msg.Chat, lang, true)
	return err
}

func (sc *SpamControl) getSpamCase(ctx context.Context, msg *api.Message, preVoteRestricted bool) (*db.SpamCase, error) {
	spamCase, err := sc.store.GetActiveSpamCase(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		log.WithField("error", err.Error()).Debug("failed to get active spam case")
	}
	if spamCase != nil && spamCase.MessageID != 0 && spamCase.MessageID != msg.MessageID {
		spamCase, err = sc.store.GetActiveSpamCaseByMessage(ctx, msg.Chat.ID, msg.From.ID, msg.MessageID)
		if err != nil {
			log.WithField("error", err.Error()).Debug("failed to get active message-bound spam case")
			spamCase = nil
		}
	}
	if spamCase != nil && preVoteRestricted && !spamCase.PreVoteRestricted {
		spamCase = nil
	}
	if spamCase == nil {
		now := time.Now()
		var resolveAt *time.Time
		if preVoteRestricted {
			value := now.Add(sc.effectiveVotingPolicy(ctx, msg.Chat.ID).Timeout)
			resolveAt = &value
		}
		spamCase, err = sc.store.CreateSpamCase(ctx, &db.SpamCase{
			ChatID:            msg.Chat.ID,
			UserID:            msg.From.ID,
			MessageID:         msg.MessageID,
			MessageText:       bot.ExtractContentFromMessage(msg),
			CreatedAt:         now,
			Status:            spamCaseStatusPending,
			PreVoteRestricted: preVoteRestricted,
			ResolveAt:         resolveAt,
		})
		if err != nil {
			log.WithField("error", err.Error()).Debug("failed to create spam case")
			return nil, fmt.Errorf("failed to create spam case: %w", err)
		}
	}
	return spamCase, nil
}

type ProcessingResult struct {
	MessageDeleted bool
	UserBanned     bool
	Error          string
}

func (sc *SpamControl) getReportedSpamCase(ctx context.Context, targetMsg *api.Message) (*db.SpamCase, error) {
	spamCase, err := sc.store.GetActiveSpamCaseByMessage(ctx, targetMsg.Chat.ID, targetMsg.From.ID, targetMsg.MessageID)
	if err != nil {
		log.WithField("error", err.Error()).Debug("failed to get active message-bound spam case")
	}
	if spamCase == nil {
		now := time.Now()
		resolveAt := now.Add(sc.effectiveVotingPolicy(ctx, targetMsg.Chat.ID).Timeout)
		spamCase, err = sc.store.CreateSpamCase(ctx, &db.SpamCase{
			ChatID:            targetMsg.Chat.ID,
			UserID:            targetMsg.From.ID,
			MessageID:         targetMsg.MessageID,
			MessageText:       bot.ExtractContentFromMessage(targetMsg),
			CreatedAt:         now,
			Status:            spamCaseStatusPending,
			PreVoteRestricted: false,
			ResolveAt:         &resolveAt,
		})
		if err != nil {
			log.WithField("error", err.Error()).Debug("failed to create reported spam case")
			return nil, fmt.Errorf("failed to create reported spam case: %w", err)
		}
	}
	return spamCase, nil
}

func (sc *SpamControl) ProcessReportedMessage(ctx context.Context, targetMsg *api.Message, reportMsg *api.Message, chat *api.Chat, lang string) (*ProcessingResult, error) {
	result := &ProcessingResult{}
	if targetMsg == nil || reportMsg == nil || chat == nil || targetMsg.From == nil {
		return result, nil
	}

	spamCase, err := sc.getReportedSpamCase(ctx, targetMsg)
	if err != nil {
		return result, err
	}

	if err := sc.store.AddSpamCaseReportMessage(ctx, &db.SpamCaseReportMessage{
		CaseID:    spamCase.ID,
		ChatID:    reportMsg.Chat.ID,
		MessageID: reportMsg.MessageID,
		CreatedAt: time.Now(),
	}); err != nil {
		log.WithField("error", err.Error()).Error("failed to record report message")
	}

	if spamCase.NotificationMessageID == 0 && spamCase.ChannelPostID == 0 {
		notifMsg := sc.createInChatNotification(targetMsg, spamCase.ID, lang, true)
		notification, err := sc.sendNotificationWithQuoteFallback(ctx, notifMsg)
		if err != nil {
			log.WithField("error", err.Error()).Error("failed to send reported spam voting prompt")
		} else {
			spamCase.NotificationMessageID = notification.MessageID
			if err := sc.store.UpdateSpamCasePresentation(ctx, spamCase); err != nil {
				sc.closeVotingPrompt(ctx, spamCase)
				return result, fmt.Errorf("persist reported spam voting surface: %w", err)
			}
		}

	}

	return result, nil
}

func (sc *SpamControl) preprocessMessage(ctx context.Context, msg *api.Message, chat *api.Chat, lang string, voting bool) (*ProcessingResult, error) {
	result := &ProcessingResult{}
	var persistenceErr error
	if msg == nil || chat == nil || msg.From == nil {
		return result, nil
	}

	spamCase, err := sc.getSpamCase(ctx, msg, voting)
	if err != nil {
		return result, err
	}

	shouldNotify := spamCase.NotificationMessageID == 0 && spamCase.ChannelPostID == 0
	votingSurfaceReady := !voting || !shouldNotify
	if shouldNotify {
		var notifMsg api.Chattable
		if sc.config.LogChannelUsername != "" {
			channelMsg, err := sc.SendChannelPost(ctx, msg, lang, voting)
			if err != nil {
				log.WithField("error", err.Error()).Error("failed to send channel post")
				notifMsg = sc.createInChatNotification(msg, spamCase.ID, lang, voting)
			} else if channelMsg != nil && channelMsg.MessageID != 0 {
				votingSurfaceReady = true
				spamCase.ChannelUsername = sc.config.LogChannelUsername
				spamCase.ChannelPostID = channelMsg.MessageID
			}
			if sc.verbose && channelMsg != nil && channelMsg.MessageID != 0 {
				channelPostLink := fmt.Sprintf("https://t.me/%s/%d", sc.config.LogChannelUsername, channelMsg.MessageID)
				notifMsg = sc.createChannelNotification(msg, channelPostLink, lang)
			}
		} else {
			notifMsg = sc.createInChatNotification(msg, spamCase.ID, lang, voting)
		}

		if notifMsg != nil {
			notification, err := sc.sendNotificationWithQuoteFallback(ctx, notifMsg)
			if err != nil {
				log.WithField("error", err.Error()).Error("failed to send notification")
			} else {
				spamCase.NotificationMessageID = notification.MessageID
				if voting && notification.MessageID != 0 && spamCase.ChannelPostID == 0 {
					votingSurfaceReady = true
				}
				if !voting {
					sc.scheduleAfter(sc.config.SuspectNotificationTimeout, func(runCtx context.Context) {
						select {
						case <-runCtx.Done():
							return
						default:
						}
						if _, err := sc.bot.RequestWithContext(runCtx, api.NewDeleteMessage(msg.Chat.ID, notification.MessageID)); err != nil {
							log.WithField("error", err.Error()).Error("failed to delete notification")
						}
					})
				}
			}
		}
		if err := sc.store.UpdateSpamCasePresentation(ctx, spamCase); err != nil {
			if voting {
				sc.closeVotingPrompt(ctx, spamCase)
				return result, fmt.Errorf("persist voting surface: %w", err)
			}
			log.WithField("error", err.Error()).Error("failed to persist notification presentation")
		}
	}
	if voting && !votingSurfaceReady {
		return result, errors.New("no voting surface is available")
	}

	if voting {
		if err := sc.banService.MuteUser(ctx, chat.ID, msg.From.ID); err != nil {
			spamCase.PreVoteRestricted = false
			if updateErr := sc.store.SetSpamCasePreVoteRestricted(ctx, spamCase.ID, false); updateErr != nil {
				persistenceErr = fmt.Errorf("record failed pre-vote restriction: %w", updateErr)
			}
			if isTelegramPrivilegeError(err) {
				result.Error = errChatAdminRequired
			} else {
				result.Error = err.Error()
			}
		} else {
			result.UserBanned = true
			if err := bot.DeleteChatMessage(ctx, sc.bot, chat.ID, msg.MessageID); err != nil {
				log.WithField("error", err.Error()).WithField("chat_title", chat.Title).WithField("chat_username", chat.UserName).Error("failed to delete message")
			} else {
				result.MessageDeleted = true
			}
		}
	} else {
		claimedCase, claimed, err := sc.store.ClaimKnownSpamCase(ctx, spamCase.ID, time.Now())
		if err != nil {
			return result, fmt.Errorf("claim known spam case: %w", err)
		}
		if !claimed {
			return result, nil
		}
		spamCase = claimedCase
		if err := sc.resolveClaimedCase(ctx, spamCase); err != nil {
			log.WithField("error", err.Error()).WithField("chat_title", chat.Title).WithField("chat_username", chat.UserName).Error("failed to resolve known spam case")
			if isTelegramPrivilegeError(err) {
				result.Error = errChatAdminRequired
			} else {
				result.Error = err.Error()
			}
		} else {
			result.UserBanned = true
			result.MessageDeleted = true
		}
	}

	if result.Error == errChatAdminRequired {
		unsuccessReply := api.NewMessage(chat.ID, "I don't have enough rights to ban this user")
		unsuccessReply.ReplyParameters = api.ReplyParameters{
			ChatID:                   chat.ID,
			MessageID:                msg.MessageID,
			AllowSendingWithoutReply: true,
		}
		unsuccessReply.DisableNotification = true
		unsuccessReply.LinkPreviewOptions.IsDisabled = true
		apiResult, err := bot.Send(ctx, sc.bot, unsuccessReply)
		if err != nil {
			log.WithField("error", err.Error()).Error("failed to send unsuccess reply")
		}
		if apiResult.MessageID != 0 {
			sc.scheduleAfter(sc.config.SuspectNotificationTimeout, func(runCtx context.Context) {
				select {
				case <-runCtx.Done():
					return
				default:
				}
				if _, err := sc.bot.RequestWithContext(runCtx, api.NewDeleteMessage(chat.ID, apiResult.MessageID)); err != nil {
					log.WithField("error", err.Error()).Error("failed to delete unsuccess reply")
				}
			})
		}
	}

	return result, persistenceErr
}

func (sc *SpamControl) ProcessBannedMessage(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*ProcessingResult, error) {
	return sc.preprocessMessage(ctx, msg, chat, lang, false)
}

func (sc *SpamControl) ProcessSpamMessage(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*ProcessingResult, error) {
	return sc.preprocessMessage(ctx, msg, chat, lang, true)
}

func (sc *SpamControl) SendChannelPost(ctx context.Context, msg *api.Message, lang string, voting bool) (*api.Message, error) {
	spamCase, err := sc.getSpamCase(ctx, msg, voting)
	if err != nil {
		return nil, fmt.Errorf("failed to get spam case: %w", err)
	}
	channelMsg := sc.createChannelPost(msg, spamCase.ID, lang, voting)
	sent, err := bot.Send(ctx, sc.bot, channelMsg)
	if err != nil {
		log.WithField("error", err.Error()).Error("failed to send channel post")
		return nil, err
	}
	spamCase.ChannelUsername = sc.config.LogChannelUsername
	spamCase.ChannelPostID = sent.MessageID

	return &sent, nil
}

func (sc *SpamControl) createInChatNotification(msg *api.Message, caseID int64, lang string, voting bool) api.Chattable {
	text := fmt.Sprintf(
		i18n.Get("⚠️ Potential spam message from %s\n\nMessage: %s\n\nPlease vote:", lang),
		bot.GetUN(msg.From),
		bot.ExtractContentFromMessage(msg),
	)

	replyMsg := api.NewMessage(msg.Chat.ID, text)
	if voting {
		markup := api.NewInlineKeyboardMarkup(
			api.NewInlineKeyboardRow(
				api.NewInlineKeyboardButtonData("✅ "+i18n.Get("Not Spam", lang), fmt.Sprintf("spam_vote:%d:0", caseID)),
				api.NewInlineKeyboardButtonData("🚫 "+i18n.Get("Spam", lang), fmt.Sprintf("spam_vote:%d:1", caseID)),
			),
		)
		replyMsg.ReplyMarkup = &markup
	}

	replyMsg.MessageThreadID = msg.MessageThreadID
	replyMsg.ReplyParameters = targetReplyParameters(msg)
	replyMsg.DisableNotification = true
	replyMsg.LinkPreviewOptions.IsDisabled = true
	return replyMsg
}

func (sc *SpamControl) createChannelPost(msg *api.Message, caseID int64, lang string, voting bool) api.Chattable {
	from := bot.GetUN(msg.From)
	textSlice := strings.Split(bot.ExtractContentFromMessage(msg), "\n")
	for i, line := range textSlice {
		line = strings.ReplaceAll(line, "http", "_ttp")
		line = strings.ReplaceAll(line, "+7", "+*")

		line = api.EscapeText(api.ModeMarkdownV2, line)
		line = regexp.MustCompile(`@(\w+)`).ReplaceAllString(line, "@**")
		textSlice[i] = line
	}
	text := fmt.Sprintf(
		">%s\n**>%s",
		api.EscapeText(api.ModeMarkdownV2, from),
		strings.Join(textSlice, "\n>"),
	)
	channelMsg := api.NewMessageToChannel("@"+strings.TrimPrefix(sc.config.LogChannelUsername, "@"), text)

	if voting {
		markup := api.NewInlineKeyboardMarkup(
			api.NewInlineKeyboardRow(
				api.NewInlineKeyboardButtonData("✅ "+i18n.Get("Not Spam", lang), fmt.Sprintf("spam_vote:%d:0", caseID)),
				api.NewInlineKeyboardButtonData("🚫 "+i18n.Get("Spam", lang), fmt.Sprintf("spam_vote:%d:1", caseID)),
			),
		)
		channelMsg.ReplyMarkup = &markup
	}

	channelMsg.ParseMode = api.ModeMarkdownV2
	return channelMsg
}

func (sc *SpamControl) createChannelNotification(msg *api.Message, channelPostLink string, lang string) api.Chattable {
	from := bot.GetUN(msg.From)
	text := fmt.Sprintf(i18n.Get("Message from %s is being reviewed for spam\n\nAppeal here: [link](%s)", lang), from, channelPostLink)
	notificationMsg := api.NewMessage(msg.Chat.ID, text)
	notificationMsg.ParseMode = api.ModeMarkdown
	notificationMsg.MessageThreadID = msg.MessageThreadID
	notificationMsg.ReplyParameters = targetReplyParameters(msg)
	notificationMsg.DisableNotification = true
	notificationMsg.LinkPreviewOptions.IsDisabled = true

	return notificationMsg
}

func (sc *SpamControl) sendNotificationWithQuoteFallback(ctx context.Context, notifMsg api.Chattable) (api.Message, error) {
	notification, err := bot.Send(ctx, sc.bot, notifMsg)
	if err == nil || !isReplyQuoteRejected(err) {
		return notification, err
	}
	retryMsg, ok := chattableWithoutReplyQuote(notifMsg)
	if !ok {
		return notification, err
	}
	return bot.Send(ctx, sc.bot, retryMsg)
}

func isReplyQuoteRejected(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "quote")
}

func chattableWithoutReplyQuote(chattable api.Chattable) (api.Chattable, bool) {
	switch msg := chattable.(type) {
	case api.MessageConfig:
		msg.ReplyParameters.Quote = ""
		msg.ReplyParameters.QuoteParseMode = ""
		msg.ReplyParameters.QuoteEntities = nil
		msg.ReplyParameters.QuotePosition = 0
		return msg, true
	default:
		return nil, false
	}
}

func targetReplyParameters(msg *api.Message) api.ReplyParameters {
	if msg == nil {
		return api.ReplyParameters{}
	}
	return api.ReplyParameters{
		MessageID:                msg.MessageID,
		ChatID:                   msg.Chat.ID,
		AllowSendingWithoutReply: true,
		Quote:                    targetReplyQuote(msg),
	}
}

func targetReplyQuote(msg *api.Message) string {
	if msg == nil {
		return ""
	}
	quote := strings.TrimSpace(msg.Text)
	if quote == "" {
		quote = strings.TrimSpace(msg.Caption)
	}
	if quote == "" {
		return ""
	}
	runes := []rune(quote)
	if len(runes) > maxReplyQuoteRunes {
		return string(runes[:maxReplyQuoteRunes])
	}
	return quote
}

func (sc *SpamControl) DeleteMessageAfter(chatID int64, messageID int, delay time.Duration) {
	if messageID == 0 {
		return
	}
	sc.scheduleAfter(delay, func(runCtx context.Context) {
		if err := bot.DeleteChatMessage(runCtx, sc.bot, chatID, messageID); err != nil {
			log.WithField("error", err.Error()).WithField("chat_id", chatID).WithField("message_id", messageID).Error("failed to delete scheduled message")
		}
	})
}

func (sc *SpamControl) cleanupRecentJoinMessage(ctx context.Context, chatID, userID int64) {
	joiners, err := sc.store.GetChatRecentJoiners(ctx, chatID)
	if err != nil {
		log.WithField("error", err.Error()).WithField("chat_id", chatID).WithField("user_id", userID).Error("failed to get recent joiners for cleanup")
		return
	}

	for _, joiner := range joiners {
		if joiner == nil || joiner.UserID != userID {
			continue
		}
		if joiner.JoinMessageID != 0 {
			if err := bot.DeleteChatMessage(ctx, sc.bot, chatID, joiner.JoinMessageID); err != nil {
				log.WithField("error", err.Error()).WithField("chat_id", chatID).WithField("user_id", userID).WithField("message_id", joiner.JoinMessageID).Error("failed to delete recent join message")
			}
		}
		if err := sc.store.ProcessRecentJoiner(ctx, chatID, userID, true); err != nil {
			log.WithField("error", err.Error()).WithField("chat_id", chatID).WithField("user_id", userID).Error("failed to mark recent joiner as spammer")
		}
		return
	}
}

func (sc *SpamControl) getLogEntry() *log.Entry {
	return log.WithField("object", "SpamControl")
}

func (sc *SpamControl) clearKnownNonMember(ctx context.Context, chatID int64, userID int64) {
	if err := sc.store.DeleteChatKnownNonMember(ctx, chatID, userID); err != nil {
		log.WithField("error", err.Error()).Error("failed to delete known non-member")
	}
}

func (sc *SpamControl) scheduleAfter(delay time.Duration, task func(ctx context.Context)) {
	runCtx := sc.getRuntimeContext()
	sc.wg.Go(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-runCtx.Done():
			return
		case <-timer.C:
			task(runCtx)
		}
	})
}

func (sc *SpamControl) getRuntimeContext() context.Context {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.runtimeCtx != nil {
		return sc.runtimeCtx
	}
	return context.Background()
}
