package ngbot

import (
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/config"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type UpdateHandler interface {
	Handle(u tgbotapi.Update) (bool, error)
}

type UpdateProcessor struct {
	cfg            *config.Config
	bot            *tgbotapi.BotAPI
	updateHandlers []UpdateHandler
}

func NewUpdateProcessor(cfg *config.Config, bot *tgbotapi.BotAPI) *UpdateProcessor {
	return &UpdateProcessor{
		cfg: cfg,
		bot: bot,
	}
}

func (up *UpdateProcessor) AddUpdateHandler(uh UpdateHandler) {
	up.updateHandlers = append(up.updateHandlers, uh)
}

func (up *UpdateProcessor) Process(u tgbotapi.Update) (result error) {
	for _, handler := range up.updateHandlers {
		proceed, err := handler.Handle(u)
		if err != nil {
			return errors.Wrap(err, "handling error")
		}
		if !proceed {
			log.Debug("not proceeding")
			break
		}
	}

	return result
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
