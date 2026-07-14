package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

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
	IsSpam(ctx context.Context, message string, examples []string) (*bool, error)
	IsReportedSpam(ctx context.Context, message string, examples []string) (*bool, error)
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
	s               bot.Service
	bot             *api.BotAPI
	store           reactorStore
	stats           handlersbase.StatsStore
	config          Config
	spamDetector    SpamDetectorInterface
	banService      moderation.BanService
	spamControl     *moderation.SpamControl
	processSpam     func(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error)
	processBanned   func(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error)
	processReported func(ctx context.Context, targetMsg *api.Message, reportMsg *api.Message, chat *api.Chat, lang string) (*moderation.ProcessingResult, error)
	lastResults     map[messageResultKey]*MessageProcessingResult
	resultOrder     []messageResultKey
	resultMutex     sync.Mutex
}

type reactorStore interface {
	ListChatSpamExamples(ctx context.Context, chatID int64, limit int, offset int) ([]*db.ChatSpamExample, error)
	IsChatNotSpammer(ctx context.Context, chatID int64, userID int64, username string) (bool, error)
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
	}
	r.getLogEntry().Debug("created new reactor")
	return r
}

func (r *Reactor) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
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

	if u.Message != nil {
		if user == nil {
			if err := r.handleMessage(ctx, u.Message, chat, nil, settings); err != nil {
				entry.WithError(err).Warn("failed to classify anonymous sender chat message")
			}
			return true, nil
		}
		if u.Message.IsCommand() {
			if err := r.handleCommand(ctx, u.Message, chat, user, settings); err != nil {
				entry.WithField(logFieldError, err.Error()).Error("error handling message")
				return true, err
			}
			return true, nil
		}
		if messageMentionsCurrentBot(u.Message, r.bot.Self) {
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
	}

	return true, nil
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

	notSpamVotes, spamVotes, err := r.spamControl.RecordVote(ctx, caseID, user.ID, vote)
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
		entry.WithField(logFieldError, err.Error()).Error("failed to record spam vote")
		return true, nil
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	text := fmt.Sprintf(i18n.Get("Votes: ✅ %d | 🚫 %d", language), notSpamVotes, spamVotes)

	edit := api.NewEditMessageText(chat.ID, u.CallbackQuery.Message.MessageID, text)
	edit.ReplyMarkup = u.CallbackQuery.Message.ReplyMarkup
	if _, err := bot.Send(ctx, r.bot, edit); err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to update vote count")
	}

	_, err = r.bot.RequestWithContext(ctx, api.NewCallback(u.CallbackQuery.ID, i18n.Get("✓ Vote recorded", language)))
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to acknowledge callback")
	}

	return true, nil
}

func (r *Reactor) validateUpdate(u *api.Update, chat *api.Chat, user *api.User) error {
	if u == nil {
		return errors.New("nil update")
	}

	if u.Message != nil {
		if chat == nil {
			return errors.New("nil chat")
		}
		if user == nil && u.Message.SenderChat == nil {
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
	return log.WithField("object", "Reactor")
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
