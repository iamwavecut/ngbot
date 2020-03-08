package bot

import (
	"github.com/iamwavecut/ngbot/db"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type Handler interface {
	Handle(u *tgbotapi.Update, cm *db.ChatMeta) (proceed bool, err error)
}

type UpdateProcessor struct {
	s              Service
	updateHandlers []Handler
	chatMetaCache  map[int64]*db.ChatMeta
}

var registeredHandlers = make(map[string]Handler)

func RegisterUpdateHandler(title string, handler Handler) {
	registeredHandlers[title] = handler
}

func NewUpdateProcessor(s Service) *UpdateProcessor {
	enabledHandlers := make([]Handler, 0)
	for _, handlerName := range s.GetConfig().EnabledHandlers {
		if _, ok := registeredHandlers[handlerName]; !ok || registeredHandlers[handlerName] == nil {
			log.Warnf("no registered handler: %s", handlerName)
			continue
		}
		enabledHandlers = append(enabledHandlers, registeredHandlers[handlerName])
	}

	return &UpdateProcessor{
		s:              s,
		updateHandlers: enabledHandlers,
		chatMetaCache:  make(map[int64]*db.ChatMeta),
	}
}

func (up *UpdateProcessor) Process(u *tgbotapi.Update) (result error) {
	chat, err := up.GetChat(u)
	if err != nil {
		log.WithError(err).WithField("update", *u).Warn("cant get chat from update")
	}

	cm, err := up.GetChatMeta(chat.ID)
	if err != nil {
		return errors.WithMessage(err, "cant get chat meta")
	}
	updatedChatMeta := false
	if cm.Title != chat.Title {
		cm.Title = chat.Title
		updatedChatMeta = true
	}
	if cm.Type != chat.Type {
		cm.Type = chat.Type
		updatedChatMeta = true
	}
	if updatedChatMeta {
		if uErr := up.s.GetDB().UpsertChatMeta(cm); uErr != nil {
			log.WithError(uErr).Warn("cant update chat title")
		}
	}

	for _, handler := range up.updateHandlers {
		proceed, err := handler.Handle(u, up.chatMetaCache[chat.ID])
		if err != nil {
			result = errors.WithMessage(err, "handling error")
		}
		if !proceed {
			log.Trace("not proceeding")
			return
		}
	}
	return
}

func (up *UpdateProcessor) GetChat(u *tgbotapi.Update) (*tgbotapi.Chat, error) {
	if u == nil {
		return nil, errors.New("nil update")
	}
	switch {
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil && u.CallbackQuery.Message.Chat != nil:
		return u.CallbackQuery.Message.Chat, nil
	case u.Message != nil && u.Message.Chat != nil:
		return u.Message.Chat, nil
	case u.EditedMessage != nil && u.EditedMessage.Chat != nil:
		return u.EditedMessage.Chat, nil
	}
	return nil, errors.New("no chat")
}

func (up *UpdateProcessor) GetChatMeta(chatID int64) (*db.ChatMeta, error) {
	if _, ok := up.chatMetaCache[chatID]; ok {
		return up.chatMetaCache[chatID], nil
	}
	cm, err := up.s.GetDB().GetChatMeta(chatID)
	if err != nil {
		log.WithError(err).Warn("cant get chat meta")
	}
	up.chatMetaCache[chatID] = cm
	return cm, nil
}

func KickUserFromChat(bot *tgbotapi.BotAPI, userID int, chatID int64) error {
	log.WithField("context", "bot")
	_, err := bot.Request(tgbotapi.KickChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{
			ChatID: chatID,
			UserID: userID,
		},
		UntilDate: time.Now().Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return errors.WithMessage(err, "cant kick")
	}

	return nil
}

func GetFullName(user *tgbotapi.User) (string, int) {
	userId := user.ID
	userName := user.FirstName + " " + user.LastName
	userName = strings.TrimSpace(userName)
	if 0 == len(userName) {
		userName = user.UserName
	}

	return userName, userId
}

func GetUN(user *tgbotapi.User) (string, int) {
	userId := user.ID
	userName := user.UserName
	if 0 == len(userName) {
		userName = user.FirstName + " " + user.LastName
		userName = strings.TrimSpace(userName)
	}

	return userName, userId
}

func GetTitle(chat *tgbotapi.Chat) string {
	if chat == nil {
		return ""
	}
	switch chat.Type {
	case "private":
		return "p2p"
	case "supergroup", "group", "channel":
		return chat.Title
	default:
		log.Warn("unknown chat type", chat.Type)
	}

	return ""
}
