package bot

import (
	"strings"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/config"
)

type (
	Handler interface {
		Handle(u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
	}

	UpdateProcessor struct {
		s              Service
		updateHandlers []Handler
	}
)

var registeredHandlers = make(map[string]Handler)

func RegisterUpdateHandler(title string, handler Handler) {
	registeredHandlers[title] = handler
}

func NewUpdateProcessor(s Service) *UpdateProcessor {
	enabledHandlers := make([]Handler, 0)
	for _, handlerName := range config.Get().EnabledHandlers {
		if _, ok := registeredHandlers[handlerName]; !ok || registeredHandlers[handlerName] == nil {
			log.Warnf("no registered handler: %s", handlerName)
			continue
		}
		enabledHandlers = append(enabledHandlers, registeredHandlers[handlerName])
	}

	return &UpdateProcessor{
		s:              s,
		updateHandlers: enabledHandlers,
	}
}

func (up *UpdateProcessor) Process(u *api.Update) error {
	chat := u.FromChat()
	if chat == nil && u.ChatJoinRequest != nil {
		chat = &u.ChatJoinRequest.Chat
	}

	user := u.SentFrom()
	if user == nil && u.ChatJoinRequest != nil {
		user = &u.ChatJoinRequest.From
	}

	for _, handler := range up.updateHandlers {
		proceed, err := handler.Handle(u, chat, user)
		if err != nil {
			return errors.WithMessage(err, "handling error")
		}
		if !proceed {
			log.Trace("not proceeding")
			break
		}
	}
	return nil
}

func DeleteChatMessage(bot *api.BotAPI, chatID int64, messageID int) error {
	if _, err := bot.Request(api.NewDeleteMessage(chatID, messageID)); err != nil {
		return err
	}
	return nil
}

func BanUserFromChat(bot *api.BotAPI, userID int64, chatID int64) error {
	if _, err := bot.Request(api.BanChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate:      time.Now().Add(10 * time.Minute).Unix(),
		RevokeMessages: true,
	}); err != nil {
		return errors.WithMessage(err, "cant kick")
	}
	return nil
}

func RestrictChatting(bot *api.BotAPI, userID int64, chatID int64) error {
	if _, err := bot.Request(api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate: time.Now().Add(10 * time.Minute).Unix(),
		Permissions: &api.ChatPermissions{
			CanSendMessages:       false,
			CanSendAudios:         false,
			CanSendDocuments:      false,
			CanSendPhotos:         false,
			CanSendVideos:         false,
			CanSendVideoNotes:     false,
			CanSendVoiceNotes:     false,
			CanSendPolls:          false,
			CanSendOtherMessages:  false,
			CanAddWebPagePreviews: false,
			CanChangeInfo:         false,
			CanInviteUsers:        false,
			CanPinMessages:        false,
			CanManageTopics:       false,
		},
	}); err != nil {
		return errors.WithMessage(err, "cant restrict")
	}
	return nil
}

func UnrestrictChatting(bot *api.BotAPI, userID int64, chatID int64) error {
	if _, err := bot.Request(api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
		UntilDate: time.Now().Add(10 * time.Minute).Unix(),
		Permissions: &api.ChatPermissions{
			CanSendMessages:       true,
			CanSendAudios:         true,
			CanSendDocuments:      true,
			CanSendPhotos:         true,
			CanSendVideos:         true,
			CanSendVideoNotes:     true,
			CanSendVoiceNotes:     true,
			CanSendPolls:          true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
			CanChangeInfo:         true,
			CanInviteUsers:        true,
			CanPinMessages:        true,
			CanManageTopics:       true,
		},
	}); err != nil {
		return errors.WithMessage(err, "cant unrestrict")
	}
	return nil
}

func ApproveJoinRequest(bot *api.BotAPI, userID int64, chatID int64) error {
	if _, err := bot.Request(api.ApproveChatJoinRequestConfig{
		ChatConfig: api.ChatConfig{
			ChatID: chatID,
		},
		UserID: userID,
	}); err != nil {
		return errors.WithMessage(err, "cant accept join request")
	}
	return nil
}

func DeclineJoinRequest(bot *api.BotAPI, userID int64, chatID int64) error {
	if _, err := bot.Request(api.DeclineChatJoinRequest{
		ChatConfig: api.ChatConfig{
			ChatID: chatID,
		},
		UserID: userID,
	}); err != nil {
		return errors.WithMessage(err, "cant accept join request")
	}
	return nil
}

func GetUpdatesChans(bot *api.BotAPI, config api.UpdateConfig) (api.UpdatesChannel, chan error) {
	ch := make(chan api.Update, bot.Buffer)
	chErr := make(chan error)

	go func() {
		defer close(ch)
		defer close(chErr)
		for {
			updates, err := bot.GetUpdates(config)
			if err != nil {
				chErr <- err
				return
			}

			for _, update := range updates {
				if update.UpdateID >= config.Offset {
					config.Offset = update.UpdateID + 1
					ch <- update
				}
			}
		}
	}()

	return ch, chErr
}

func GetUN(user *api.User) string {
	if user == nil {
		return ""
	}
	userName := user.UserName
	if 0 == len(userName) {
		userName = user.FirstName + " " + user.LastName
		userName = strings.TrimSpace(userName)
	}
	return userName
}

func GetFullName(user *api.User) string {
	if user == nil {
		return ""
	}
	fullName := user.FirstName + " " + user.LastName
	fullName = strings.TrimSpace(fullName)
	if 0 == len(fullName) {
		fullName = user.UserName
	}
	return fullName
}
