package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type Admin struct {
	s         bot.Service
	store     adminStore
	languages []string
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.Mutex
	started   bool
}

type adminStore interface {
	SetChatBotMembership(ctx context.Context, membership *db.ChatBotMembership) error
	GetChatBotMembership(ctx context.Context, chatID int64) (*db.ChatBotMembership, error)
	UpsertChatManager(ctx context.Context, manager *db.ChatManager) error
	GetChatManager(ctx context.Context, chatID int64, userID int64) (*db.ChatManager, error)

	CreateAdminPanelSession(ctx context.Context, session *db.AdminPanelSession) (*db.AdminPanelSession, error)
	GetAdminPanelSession(ctx context.Context, id int64) (*db.AdminPanelSession, error)
	GetAdminPanelSessionByUserChat(ctx context.Context, userID int64, chatID int64) (*db.AdminPanelSession, error)
	GetAdminPanelSessionByUserPage(ctx context.Context, userID int64, page string) (*db.AdminPanelSession, error)
	UpdateAdminPanelSession(ctx context.Context, session *db.AdminPanelSession) error
	DeleteAdminPanelSession(ctx context.Context, id int64) error
	GetExpiredAdminPanelSessions(ctx context.Context, before time.Time) ([]*db.AdminPanelSession, error)

	CreateAdminPanelCommand(ctx context.Context, cmd *db.AdminPanelCommand) (*db.AdminPanelCommand, error)
	GetAdminPanelCommand(ctx context.Context, id int64) (*db.AdminPanelCommand, error)
	DeleteAdminPanelCommandsBySession(ctx context.Context, sessionID int64) error

	CreateChatSpamExample(ctx context.Context, example *db.ChatSpamExample) (*db.ChatSpamExample, error)
	GetChatSpamExample(ctx context.Context, id int64) (*db.ChatSpamExample, error)
	ListChatSpamExamples(ctx context.Context, chatID int64, limit int, offset int) ([]*db.ChatSpamExample, error)
	CountChatSpamExamples(ctx context.Context, chatID int64) (int, error)
	DeleteChatSpamExample(ctx context.Context, id int64) error
}

func NewAdmin(s bot.Service) *Admin {
	entry := log.WithField("object", "Admin").WithField("method", "NewAdmin")

	a := &Admin{
		s:         s,
		store:     s.GetDB(),
		languages: i18n.GetLanguagesList(),
	}
	entry.Debug("created new admin handler")
	return a
}

func (a *Admin) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.startPanelCleanup(runCtx)
	a.started = true
	return nil
}

func (a *Admin) Stop(ctx context.Context) error {
	a.mu.Lock()
	if !a.started {
		a.mu.Unlock()
		return nil
	}
	a.started = false
	cancel := a.cancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (a *Admin) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	entry := a.getLogEntry().WithField("method", "Handle")

	if u == nil {
		return true, nil
	}

	if u.MyChatMember != nil {
		if err := a.handleMyChatMember(ctx, u.MyChatMember); err != nil {
			entry.WithField("error", err.Error()).Error("failed to handle my_chat_member update")
			return false, err
		}
		return false, nil
	}

	if u.CallbackQuery != nil {
		handled, err := a.handlePanelCallback(ctx, u.CallbackQuery, chat, user)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to handle callback")
			return false, err
		}
		if handled {
			return false, nil
		}
		return true, nil
	}

	if u.Message == nil || user == nil || chat == nil {
		entry.Debug("chat or user is nil, proceeding")
		return true, nil
	}

	language := a.s.GetLanguage(ctx, chat.ID, user)

	if u.Message.IsCommand() {
		entry.Debugf("processing command: %s", u.Message.Command())
		switch u.Message.Command() {
		case "lang":
			isAdmin, err := a.isAdmin(chat.ID, user.ID)
			if err != nil {
				entry.WithField("error", err.Error()).Error("can't check admin status")
				return true, err
			}
			entry.Debugf("user is admin: %v", isAdmin)
			return a.handleLangCommand(ctx, u.Message, isAdmin, language)
		case "settings":
			return false, a.handleSettingsCommand(ctx, u.Message, chat, user)
		case "start":
			payload := strings.TrimSpace(u.Message.CommandArguments())
			if strings.HasPrefix(payload, "settings_") {
				return false, a.handleStartSettings(ctx, u.Message, chat, user, payload)
			}
			return a.handleStartCommand(u.Message, language)
		default:
			entry.Debug("unknown command")
			return true, nil
		}
	}

	handled, err := a.handlePanelInput(ctx, u.Message, chat, user)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to handle panel input")
		return false, err
	}
	if handled {
		return false, nil
	}
	return true, nil
}

func (a *Admin) handleLangCommand(ctx context.Context, msg *api.Message, isAdmin bool, language string) (bool, error) {
	entry := a.getLogEntry().WithField("command", "lang")
	if !isAdmin {
		entry.Debug("user is not admin, ignoring command")
		return true, nil
	}

	argument := msg.CommandArguments()
	entry.Debugf("language argument: %s", argument)

	isAllowed := false
	for _, allowedLanguage := range a.languages {
		if allowedLanguage == argument {
			isAllowed = true
			break
		}
	}
	if !isAllowed {
		entry.Debug("invalid language argument")
		msg := api.NewMessage(
			msg.Chat.ID,
			i18n.Get("You should use one of the following options", language)+": `"+strings.Join(a.languages, "`, `")+"`",
		)
		msg.ParseMode = api.ModeMarkdown
		msg.DisableNotification = true
		_, _ = a.s.GetBot().Send(msg)
		return false, nil
	}

	settings, err := a.s.GetSettings(ctx, msg.Chat.ID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("can't get chat settings")
		return true, err
	}
	if settings == nil {
		settings = db.DefaultSettings(msg.Chat.ID)
	}
	settings.Language = argument
	if err := a.s.SetSettings(ctx, settings); err != nil {
		entry.WithField("error", err.Error()).Error("can't update chat language")
		return false, err
	}

	entry.Debug("language set successfully")
	_, _ = a.s.GetBot().Send(api.NewMessage(
		msg.Chat.ID,
		i18n.Get("Language set successfully", language),
	))

	return false, nil
}

func (a *Admin) handleStartCommand(msg *api.Message, language string) (bool, error) {
	entry := a.getLogEntry().WithField("method", "handleStartCommand")
	entry.Debug("start command received")
	_, _ = a.s.GetBot().Send(api.NewMessage(
		msg.Chat.ID,
		i18n.Get("Bot started successfully", language),
	))

	return false, nil
}

func (a *Admin) isAdmin(chatID, userID int64) (bool, error) {
	b := a.s.GetBot()
	chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			UserID: userID,
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
		},
	})
	if err != nil {
		return false, fmt.Errorf("get chat member: %w", err)
	}

	return chatMember.IsCreator() || (chatMember.IsAdministrator() && chatMember.CanRestrictMembers), nil
}

func (a *Admin) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}
