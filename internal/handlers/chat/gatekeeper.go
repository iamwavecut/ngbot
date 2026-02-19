package handlers

/*
mermaid:
graph BusinessFlow
    A[Start] --> B[Check if Gatekeeper is enabled]
    B --> C{Is Enabled?}
    C -->|Yes| D[Fetch and validate settings]
    C -->|No| E[Return]
    D --> F{Is chat public?}
    F -->|Yes| G[Restrict new member]
    F -->|No| H[Allow new member]
    G --> I[Send challenge message]
    I --> J[Wait for response]
    J -->|Correct| K[Unrestrict member]
    J -->|Incorrect or Timeout| L[Ban member]
    K --> M[Delete challenge message]
    M --> N[Send welcome message]
    N --> O[Remove from challenged users]
    L --> P[Delete challenge message]
    L -->|Ban unsuccessful| Q[Send insufficient permissions message]
    O --> R[End]
    P --> R[End]
    Q --> R[End]
    H --> R[End]
*/
import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
)

const (
	captchaSize          = 5
	maxChallengeAttempts = 3

	updateTypeCallbackQuery   updateType = "callback_query"
	updateTypeChatJoinRequest updateType = "chat_join_request"
	updateTypeNewChatMembers  updateType = "new_chat_members"
	updateTypeIgnore          updateType = "ignore"

	processNewChatMembersInterval    = 1 * time.Minute
	processExpiredChallengesInterval = 1 * time.Minute
)

type updateType string

type Gatekeeper struct {
	s          bot.Service
	store      gatekeeperStore
	config     *config.Config
	banChecker GatekeeperBanChecker

	Variants map[string]map[string]string `yaml:"variants"`

	logger         *log.Entry
	workerCancel   context.CancelFunc
	workerWG       sync.WaitGroup
	startStopMutex sync.Mutex
	started        bool
}

type GatekeeperBanChecker interface {
	CheckBan(ctx context.Context, userID int64) (bool, error)
	IsKnownBanned(userID int64) bool
	BanUserWithMessage(ctx context.Context, chatID, userID int64, messageID int) error
}

type gatekeeperStore interface {
	CreateChallenge(ctx context.Context, challenge *db.Challenge) (*db.Challenge, error)
	GetChallengeByMessage(ctx context.Context, commChatID, userID int64, challengeMessageID int) (*db.Challenge, error)
	UpdateChallenge(ctx context.Context, challenge *db.Challenge) error
	DeleteChallenge(ctx context.Context, commChatID, userID, chatID int64) error
	GetExpiredChallenges(ctx context.Context, now time.Time) ([]*db.Challenge, error)

	AddChatRecentJoiner(ctx context.Context, joiner *db.RecentJoiner) (*db.RecentJoiner, error)
	GetUnprocessedRecentJoiners(ctx context.Context) ([]*db.RecentJoiner, error)
	ProcessRecentJoiner(ctx context.Context, chatID int64, userID int64, isSpammer bool) error
}

var challengeKeys = []string{
	"Hello, %s! We want to be sure you're not a bot, so please select %s. If not, we might have to say goodbye. Thanks for understanding!",
	"Hey %s! To keep this group human-only, could you please choose %s? If you don't, we'll have to say bye-bye. Thanks for your cooperation!",
	"Greetings, %s! We're just checking to make sure you're not a robot. Can you please pick %s? If not, we'll have to let you go. Thanks for your cooperation!",
	"Hi there, %s! We like having humans in this group, so please select %s to prove you're not a bot. If you can't, we might have to remove you. Thanks in advance!",
	"Welcome, %s! We need your help to keep this group human-only. Could you please select %s? If you can't, we might have to remove you. Thanks for your understanding!",
}

var defaultCaptchaVariants = map[string]string{
	"🍎": "apple",
	"🐶": "dog",
	"🚗": "car",
	"🌟": "star",
	"🎈": "balloon",
	"📚": "book",
	"🎵": "music",
}

var privateChallengeKeys = []string{
	"Hey %s! Exciting to see you're interested in joining the group \"%s\"! We just need one more thing from you to confirm that you're human - pick %s. If you can't, we might have to say goodbye. Thanks for your cooperation!",
	"Hello there, %s! We're happy you want to join \"%s\"! We just need you to pick %s to prove that you're human. If you can't, we might have to remove you. Thanks for your understanding!",
	"Hi %s! We're thrilled you want to be part of \"%s\"! Just one more thing to make sure you're not a bot - please select %s. If you can't, we might have to say goodbye. Thanks for your cooperation!",
	"Welcome, %s! We're glad you're interested in joining \"%s\"! Please pick %s to prove that you're a human being. If you can't, we might have to remove you. Thanks for understanding!",
	"Hey %s! We're excited you want to join the group \"%s\"! Just need a quick test to make sure you're not a robot - pick %s. If you can't, we might have to say bye-bye. Thanks for your cooperation!",
	"Hi there, %s! Joining \"%s\" is fantastic news! Please pick %s to prove you're human. If you can't, we might have to let you go. Thanks for your cooperation!",
	"Hello, %s! We're glad you want to be part of \"%s\"! Just one more step - pick %s to confirm that you're not a bot. If you can't, we might have to say goodbye. Thanks for understanding!",
	"Hey %s! We're excited to see you want to join \"%s\"! To make sure you're not a robot, please select %s. If you can't, we might have to say farewell. Thanks for your cooperation!",
	"Greetings, %s! It's great you want to join \"%s\"! Please pick %s to show that you're human. If you can't, we might have to remove you. Thanks for your understanding!",
	"Hi there, %s! Welcome to the group \"%s\"! We need one more thing from you to confirm that you're human - pick %s. If you can't, we might have to let you go. Thanks for your cooperation!",
}

func NewGatekeeper(s bot.Service, config *config.Config, banChecker GatekeeperBanChecker) *Gatekeeper {
	entry := log.WithFields(log.Fields{"object": "Gatekeeper", "method": "NewGatekeeper"})

	g := &Gatekeeper{
		s:          s,
		store:      s.GetDB(),
		config:     config,
		Variants:   map[string]map[string]string{},
		banChecker: banChecker,
	}

	langs := i18n.GetLanguagesList()
	entry.WithField("languages", langs).Debug("loading challenges for languages")
	for _, lang := range langs {
		challengesData, err := resources.FS.ReadFile(infra.GetResourcesPath("gatekeeper", "challenges", lang+".yml"))
		if err != nil {
			entry.WithFields(log.Fields{"error": err, "language": lang}).Error("cant load challenges file for language")
			continue
		}

		localVariants := map[string]string{}
		if err := yaml.Unmarshal(challengesData, &localVariants); err != nil {
			entry.WithFields(log.Fields{"error": err, "language": lang}).Error("cant unmarshal challenges yaml for language")
			continue
		}
		g.Variants[lang] = localVariants
	}
	entry.Debug("created new gatekeeper")
	return g
}

func (g *Gatekeeper) Start(ctx context.Context) error {
	g.startStopMutex.Lock()
	defer g.startStopMutex.Unlock()
	if g.started {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	g.workerCancel = cancel

	g.workerWG.Add(1)
	go func() {
		defer g.workerWG.Done()
		ticker := time.NewTicker(processNewChatMembersInterval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := g.processNewChatMembers(runCtx); err != nil && !errors.Is(err, context.Canceled) {
					g.getLogEntry().WithField("error", err.Error()).Error("failed to process new chat members")
				}
			}
		}
	}()

	g.workerWG.Add(1)
	go func() {
		defer g.workerWG.Done()
		ticker := time.NewTicker(processExpiredChallengesInterval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := g.processExpiredChallenges(runCtx); err != nil && !errors.Is(err, context.Canceled) {
					g.getLogEntry().WithField("error", err.Error()).Error("failed to process expired challenges")
				}
			}
		}
	}()

	g.started = true
	return nil
}

func (g *Gatekeeper) Stop(ctx context.Context) error {
	g.startStopMutex.Lock()
	if !g.started {
		g.startStopMutex.Unlock()
		return nil
	}
	g.started = false
	cancel := g.workerCancel
	g.startStopMutex.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.workerWG.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (g *Gatekeeper) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	entry := g.getLogEntry()

	if chat == nil {
		entry.Debug("chat is nil")
		return true, nil
	}

	entry = entry.WithFields(log.Fields{
		"chat_id": chat.ID,
	})

	if user == nil {
		entry.Debug("Missing user information")
		return true, nil
	}

	entry = entry.WithFields(log.Fields{
		"user_id": user.ID,
	})

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	updateType := g.determineUpdateType(u)
	switch updateType {
	case updateTypeIgnore:
		return true, nil
	}

	switch updateType {
	case updateTypeCallbackQuery:
		if u.CallbackQuery == nil || !isGatekeeperCallbackData(u.CallbackQuery.Data) {
			return true, nil
		}
		return false, g.handleChallenge(ctx, u, chat, user)
	case updateTypeChatJoinRequest:
		joinChatID := u.ChatJoinRequest.Chat.ID
		settings, err := g.fetchAndValidateSettings(ctx, joinChatID)
		if err != nil {
			return true, err
		}
		if !settings.GatekeeperEnabled {
			entry.Debug("gatekeeper is disabled for this chat")
			return true, nil
		}
		return true, g.handleChatJoinRequest(ctx, u, settings)
	case updateTypeNewChatMembers:
		settings, err := g.fetchAndValidateSettings(ctx, chat.ID)
		if err != nil {
			return true, err
		}
		if !settings.GatekeeperEnabled {
			entry.Debug("gatekeeper is disabled for this chat")
			return true, nil
		}
		return true, g.handleNewChatMembersV2(ctx, u, chat, settings)
	default:
		entry.Debug("No specific handler matched, proceeding with default behavior")
		return true, nil
	}
}

func isGatekeeperCallbackData(data string) bool {
	parts := strings.Split(data, ";")
	if len(parts) != 2 {
		return false
	}
	if parts[0] == "" || parts[1] == "" {
		return false
	}
	if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
		return false
	}
	return true
}

func (g *Gatekeeper) determineUpdateType(u *api.Update) updateType {
	if u.CallbackQuery != nil {
		return updateTypeCallbackQuery
	}
	if u.ChatJoinRequest != nil {
		return updateTypeChatJoinRequest
	}
	if u.Message != nil {
		if u.Message.NewChatMembers != nil {
			return updateTypeNewChatMembers
		}
	}
	return updateTypeIgnore
}

func (g *Gatekeeper) fetchAndValidateSettings(ctx context.Context, chatID int64) (*db.Settings, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	settings, err := g.s.GetSettings(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	if settings == nil {
		settings = db.DefaultSettings(chatID)
	}
	return settings, nil
}

func (g *Gatekeeper) getLogEntry() *log.Entry {
	if g.logger == nil {
		g.logger = log.WithField("handler", "gatekeeper")
	}
	return g.logger
}
