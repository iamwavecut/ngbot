package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type SpamDetectorInterface interface {
	IsSpam(ctx context.Context, message string, classificationContext moderation.ClassificationContext) (*bool, error)
	IsReportedSpam(ctx context.Context, message string, classificationContext moderation.ClassificationContext) (*bool, error)
}

type Config struct {
	SpamControl config.SpamControl
}

type MessageProcessingStage string

const (
	StageInit            MessageProcessingStage = "init"
	StageMembershipCheck MessageProcessingStage = "membership_check"
	StageOverrideCheck   MessageProcessingStage = "override_check"
	StageBanCheck        MessageProcessingStage = "ban_check"
	StageContentCheck    MessageProcessingStage = "content_check"
	StageSpamCheck       MessageProcessingStage = "spam_check"
	maxLastResults                              = 1000
	maxSpamExamples                             = 20
)

type MessageProcessingActions struct {
	MessageDeleted bool
	UserBanned     bool
	Error          string
}

type MessageProcessingResult struct {
	Message    *api.Message
	Stage      MessageProcessingStage
	Skipped    bool
	SkipReason string
	IsSpam     *bool
	Actions    MessageProcessingActions
}

type messageResultKey struct {
	ChatID    int64
	MessageID int
}

type Reactor struct {
	s                bot.Service
	bot              *api.BotAPI
	store            reactorStore
	stats            handlersbase.StatsStore
	config           Config
	spamDetector     SpamDetectorInterface
	banService       moderation.BanService
	spamControl      *moderation.SpamControl
	processSpam      func(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error)
	processBanned    func(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error)
	processReported  func(ctx context.Context, targetMsg *api.Message, reportMsg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error)
	lastResults      map[messageResultKey]*MessageProcessingResult
	resultOrder      []messageResultKey
	resultMutex      sync.Mutex
	contextChatMutex sync.Mutex
	contextChats     map[int64]contextChatInfo
	now              func() time.Time
}

type reactorStore interface {
	ListChatSpamExamples(ctx context.Context, chatID int64, classification int, limit int, offset int) ([]*db.ChatSpamExample, error)
	IsChatNotSpammer(ctx context.Context, chatID int64, userID int64, username string) (bool, error)
	MessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error)
	EnsureMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error)
	RecordSafeAuthorMessage(ctx context.Context, chatID int64, author db.MessageAuthor, messageID int, now time.Time, requiredMessages int, trustDuration time.Duration, eligible bool) (*db.MessageTrust, bool, error)
	IsCheckedAuthorMessage(ctx context.Context, chatID int64, author db.MessageAuthor, messageID int) (bool, error)
	ResetMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) error
	UpsertMessageContext(ctx context.Context, record *db.MessageContext) error
	MessageContext(ctx context.Context, chatID int64, messageID int) (*db.MessageContext, error)
	RecentMessageContext(ctx context.Context, chatID int64, threadID int, beforeMessageID int, after time.Time, limit int) ([]db.MessageContext, error)
	DeleteMessageContext(ctx context.Context, chatID int64, messageID int) error
	DeleteAuthorMessageContext(ctx context.Context, chatID int64, author db.MessageAuthor) error
	IsChatKnownNonMember(ctx context.Context, chatID int64, userID int64) (bool, error)
	UpsertChatKnownNonMember(ctx context.Context, record *db.ChatKnownNonMember) error
	DeleteChatKnownNonMember(ctx context.Context, chatID int64, userID int64) error
}

func NewReactor(s bot.Service, botAPI *api.BotAPI, store reactorStore, stats handlersbase.StatsStore, banService moderation.BanService, spamControl *moderation.SpamControl, spamDetector SpamDetectorInterface, config Config) *Reactor {
	r := &Reactor{
		s:               s,
		bot:             botAPI,
		store:           store,
		stats:           stats,
		config:          config,
		banService:      banService,
		spamControl:     spamControl,
		spamDetector:    spamDetector,
		processSpam:     spamControl.ProcessSpamMessage,
		processBanned:   spamControl.ProcessBannedMessage,
		processReported: spamControl.ProcessReportedMessage,
		lastResults:     make(map[messageResultKey]*MessageProcessingResult),
		resultOrder:     make([]messageResultKey, 0, maxLastResults),
		now:             time.Now,
	}
	r.getLogEntry().Debug("created new reactor")
	return r
}

func (r *Reactor) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	ctx = withMessageContextUpdate(ctx, u)
	entry := r.getLogEntry().WithFields(log.Fields{logFieldMethod: "Handle"})
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	if err := r.validateUpdate(u, chat, user); err != nil {
		return false, err
	}

	if chat == nil {
		return true, nil
	}

	settings, err := r.getOrCreateSettings(ctx, chat)
	if err != nil {
		return false, err
	}

	if u.CallbackQuery != nil {
		return r.handleCallbackQuery(ctx, u, chat, user)
	}

	if u.MessageReaction != nil {
		return r.handleMessageReaction(ctx, u.MessageReaction, chat, settings)
	}
	if u.EditedMessage != nil {
		if err := r.handleEditedMessage(ctx, u.EditedMessage, chat, user, settings); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("error handling edited message")
			return true, err
		}
		return true, nil
	}

	if u.Message != nil {
		if user == nil {
			if err := r.handleMessage(ctx, u.Message, chat, nil, settings); err != nil {
				entry.WithError(err).Warn("failed to classify anonymous sender chat message")
				return true, err
			}
			return true, nil
		}
		if u.Message.IsCommand() {
			if err := r.handleMessageChallenge(ctx, u.Message, chat, user, settings, false, true); err != nil {
				return false, err
			}
			if r.messageWasModerated(chat.ID, u.Message.MessageID) {
				return false, nil
			}
			if err := r.handleCommand(ctx, u.Message, chat, user, settings); err != nil {
				entry.WithField(logFieldError, err.Error()).Error("error handling message")
				return true, err
			}
			return true, nil
		}
		if user != nil && messageMentionsCurrentBot(u.Message, r.bot.Self) {
			if err := r.handleMessageChallenge(ctx, u.Message, chat, user, settings, false, true); err != nil {
				return false, err
			}
			if r.messageWasModerated(chat.ID, u.Message.MessageID) {
				return false, nil
			}
			if err := r.voteBanCommand(ctx, u.Message, chat, user, settings); err != nil {
				entry.WithField(logFieldError, err.Error()).Error("error handling bot mention report")
				return true, err
			}
			return true, nil
		}
		if err := r.handleMessage(ctx, u.Message, chat, user, settings); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("error handling message")
			return true, err
		}
		if r.messageWasModerated(chat.ID, u.Message.MessageID) {
			return false, nil
		}
	}

	return true, nil
}

func (r *Reactor) handleEditedMessage(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, settings *db.Settings) error {
	if msg == nil || chat == nil || (settings != nil && !settings.LLMFirstMessageEnabled) {
		return nil
	}
	author, identified := bot.MessageAuthor(msg)
	if !identified {
		return nil
	}
	available, err := r.moderationAvailable(ctx, chat.ID)
	if err != nil {
		return bot.NewRetryableUpdateFailure(bot.UpdateFailureCapability, "capability_unknown", err)
	}
	if !available {
		return nil
	}
	if err := r.rememberMessageContext(ctx, msg, chat, settings); err != nil {
		return err
	}
	trust, err := r.store.MessageTrust(ctx, chat.ID, author)
	if err != nil {
		return fmt.Errorf("get edited message trust: %w", err)
	}
	checked, err := r.store.IsCheckedAuthorMessage(ctx, chat.ID, author, msg.MessageID)
	if err != nil {
		return fmt.Errorf("check edited message binding: %w", err)
	}
	if !checked && trust != nil && trust.Trusted(r.currentTime()) {
		return nil
	}
	return r.handleMessageChallenge(ctx, msg, chat, user, settings, true, false)
}

func (r *Reactor) messageWasModerated(chatID int64, messageID int) bool {
	result := r.GetLastProcessingResult(chatID, messageID)
	return result != nil && result.IsSpam != nil && *result.IsSpam
}

func (r *Reactor) currentTime() time.Time {
	if r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
}

func (r *Reactor) safeMessagesRequired() int {
	if r.config.SpamControl.SafeMessagesRequired > 0 {
		return r.config.SpamControl.SafeMessagesRequired
	}
	return 3
}

func (r *Reactor) authorTrustDuration() time.Duration {
	if r.config.SpamControl.AuthorTrustDuration > 0 {
		return r.config.SpamControl.AuthorTrustDuration
	}
	return 30 * 24 * time.Hour
}

func (r *Reactor) handleCallbackQuery(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{logFieldMethod: "handleCallbackQuery"})
	if user == nil {
		return true, errors.New("spam vote callback has no user")
	}
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

	vote := parts[2] == "0"

	notSpamVotes, spamVotes, err := r.spamControl.RecordVote(ctx, caseID, user.ID, user.UserName, vote)
	if err != nil {
		if errors.Is(err, moderation.ErrCommunityVotingDisabled) {
			language := "en"
			if chat != nil {
				language = r.s.GetLanguage(ctx, chat.ID, user)
			}
			_, _ = r.bot.RequestWithContext(ctx, api.NewCallback(u.CallbackQuery.ID, i18n.Get("Community voting is disabled", language)))
			return true, nil
		}
		if errors.Is(err, moderation.ErrSuspectCannotVote) {
			language := "en"
			if chat != nil {
				language = r.s.GetLanguage(ctx, chat.ID, user)
			}
			_, _ = r.bot.RequestWithContext(ctx, api.NewCallback(u.CallbackQuery.ID, i18n.Get("You cannot vote on your own spam case", language)))
			return true, nil
		}
		if errors.Is(err, moderation.ErrSpamCaseClosed) {
			_, _ = r.bot.RequestWithContext(ctx, api.NewCallback(u.CallbackQuery.ID, ""))
			return true, nil
		}
		entry.WithField(logFieldError, err.Error()).Error("failed to record spam vote")
		return false, err
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	text := fmt.Sprintf(i18n.Get("Votes: ✅ %d | 🚫 %d", language), notSpamVotes, spamVotes)

	edit := api.NewEditMessageText(chat.ID, u.CallbackQuery.Message.MessageID, text)
	edit.ReplyMarkup = u.CallbackQuery.Message.ReplyMarkup
	if _, err := bot.Send(ctx, r.bot, edit); err != nil {
		return false, fmt.Errorf("update vote count: %w", err)
	}

	_, err = r.bot.RequestWithContext(ctx, api.NewCallback(u.CallbackQuery.ID, i18n.Get("✓ Vote recorded", language)))
	if err != nil {
		return false, fmt.Errorf("acknowledge vote callback: %w", err)
	}

	return true, nil
}

func (r *Reactor) validateUpdate(u *api.Update, chat *api.Chat, user *api.User) error {
	if u == nil {
		return errors.New("nil update")
	}

	if u.Message != nil || u.EditedMessage != nil {
		msg := u.Message
		if msg == nil {
			msg = u.EditedMessage
		}
		if chat == nil {
			return errors.New("nil chat")
		}
		if user == nil && msg.SenderChat == nil {
			return errors.New("nil user")
		}
		return nil
	}

	if u.MessageReaction != nil {
		if chat == nil {
			return errors.New("nil chat or user")
		}
	}

	return nil
}

func (r *Reactor) getOrCreateSettings(ctx context.Context, chat *api.Chat) (*db.Settings, error) {
	settings, err := r.s.GetSettings(ctx, chat.ID)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		settings = db.DefaultSettings(chat.ID)
		if err := r.s.SetSettings(ctx, settings); err != nil {
			return nil, err
		}
	}
	return settings, nil
}

func (r *Reactor) getLogEntry() *log.Entry {
	return log.WithField(logFieldObject, "Reactor")
}

func (r *Reactor) storeLastResult(chatID int64, messageID int, result *MessageProcessingResult) {
	r.resultMutex.Lock()
	defer r.resultMutex.Unlock()
	if r.lastResults == nil {
		r.lastResults = make(map[messageResultKey]*MessageProcessingResult)
	}
	key := messageResultKey{ChatID: chatID, MessageID: messageID}
	if _, ok := r.lastResults[key]; !ok {
		r.resultOrder = append(r.resultOrder, key)
	}
	r.lastResults[key] = result
	if len(r.resultOrder) > maxLastResults {
		oldest := r.resultOrder[0]
		r.resultOrder = r.resultOrder[1:]
		delete(r.lastResults, oldest)
	}
}

func (r *Reactor) GetLastProcessingResult(chatID int64, messageID int) *MessageProcessingResult {
	r.resultMutex.Lock()
	defer r.resultMutex.Unlock()
	return r.lastResults[messageResultKey{ChatID: chatID, MessageID: messageID}]
}
