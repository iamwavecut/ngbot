package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type SpamDetectorInterface interface {
	IsSpam(ctx context.Context, message string, examples []string) (*bool, error)
}

type Config struct {
	FlaggedEmojis   []string
	CheckUserAPIURL string
	OpenAIModel     string
	SpamControl     config.SpamControl
}

type MessageProcessingStage string

const (
	StageInit            MessageProcessingStage = "init"
	StageMembershipCheck MessageProcessingStage = "membership_check"
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

type Reactor struct {
	s            bot.Service
	store        reactorStore
	config       Config
	spamDetector SpamDetectorInterface
	banService   moderation.BanService
	spamControl  *moderation.SpamControl
	lastResults  map[int64]*MessageProcessingResult
	resultOrder  []int64
}

type reactorStore interface {
	ListChatSpamExamples(ctx context.Context, chatID int64, limit int, offset int) ([]*db.ChatSpamExample, error)
}

func NewReactor(s bot.Service, banService moderation.BanService, spamControl *moderation.SpamControl, spamDetector SpamDetectorInterface, config Config) *Reactor {
	r := &Reactor{
		s:            s,
		store:        s.GetDB(),
		config:       config,
		banService:   banService,
		spamControl:  spamControl,
		spamDetector: spamDetector,
		lastResults:  make(map[int64]*MessageProcessingResult),
		resultOrder:  make([]int64, 0, maxLastResults),
	}
	r.getLogEntry().Debug("created new reactor")
	return r
}

func (r *Reactor) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{"method": "Handle"})
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
		return r.handleCallbackQuery(ctx, u, chat, user, settings)
	}

	if u.MessageReactionCount != nil {
		if chat == nil || user == nil {
			entry.Debug("missing chat or user for reaction update")
			return true, nil
		}
		return r.handleReaction(ctx, u.MessageReactionCount, chat, user)
	}

	if u.Message != nil {
		if u.Message.IsCommand() {
			if err := r.handleCommand(ctx, u.Message, chat, user, settings); err != nil {
				entry.WithField("error", err.Error()).Error("error handling message")
				return true, err
			}
			return true, nil
		}
		if err := r.handleMessage(ctx, u.Message, chat, user, settings); err != nil {
			entry.WithField("error", err.Error()).Error("error handling message")
			return true, err
		}
	}

	return true, nil
}

func (r *Reactor) handleCallbackQuery(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User, settings *db.Settings) (bool, error) {
	entry := r.getLogEntry().WithFields(log.Fields{"method": "handleCallbackQuery"})
	if !strings.HasPrefix(u.CallbackQuery.Data, "spam_vote:") {
		return true, nil
	}

	if settings != nil && !settings.CommunityVotingEnabled {
		var chatID int64
		if chat != nil {
			chatID = chat.ID
		} else if u.CallbackQuery.Message != nil {
			chatID = u.CallbackQuery.Message.Chat.ID
		}
		language := "en"
		if chatID != 0 {
			language = r.s.GetLanguage(ctx, chatID, user)
		}
		_, _ = r.s.GetBot().Request(api.NewCallback(u.CallbackQuery.ID, i18n.Get("Community voting is disabled", language)))
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
		entry.WithField("error", err.Error()).Error("failed to record spam vote")
		return true, nil
	}

	language := r.s.GetLanguage(ctx, chat.ID, user)
	text := fmt.Sprintf(i18n.Get("Votes: ✅ %d | 🚫 %d", language), notSpamVotes, spamVotes)

	edit := api.NewEditMessageText(chat.ID, u.CallbackQuery.Message.MessageID, text)
	edit.ReplyMarkup = u.CallbackQuery.Message.ReplyMarkup
	if _, err := r.s.GetBot().Send(edit); err != nil {
		entry.WithField("error", err.Error()).Error("failed to update vote count")
	}

	_, err = r.s.GetBot().Request(api.NewCallback(u.CallbackQuery.ID, i18n.Get("✓ Vote recorded", language)))
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to acknowledge callback")
	}

	return true, nil
}

func (r *Reactor) validateUpdate(u *api.Update, chat *api.Chat, user *api.User) error {
	if u == nil {
		return errors.New("nil update")
	}

	if u.Message != nil {
		if chat == nil || user == nil {
			return errors.New("nil chat or user")
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

func (r *Reactor) storeLastResult(messageID int64, result *MessageProcessingResult) {
	if _, ok := r.lastResults[messageID]; !ok {
		r.resultOrder = append(r.resultOrder, messageID)
	}
	r.lastResults[messageID] = result
	if len(r.resultOrder) > maxLastResults {
		oldest := r.resultOrder[0]
		r.resultOrder = r.resultOrder[1:]
		delete(r.lastResults, oldest)
	}
}

func (r *Reactor) GetLastProcessingResult(messageID int64) *MessageProcessingResult {
	return r.lastResults[messageID]
}
