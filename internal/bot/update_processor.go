package bot

import (
	"database/sql"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/event"
	"github.com/iamwavecut/ngbot/internal/infra/reg"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type (
	Handler interface {
		Handle(u *api.Update, cm *db.ChatMeta, um *db.UserMeta) (proceed bool, err error)
	}

	UpdateProcessor struct {
		s              Service
		updateHandlers []Handler
	}

	UpdateEvent struct {
		*event.Base
		payload *api.Update
	}

	Handleable interface {
		Get() *api.Update
	}
)

func (u *UpdateEvent) Get() *api.Update {
	return u.payload
}

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
	var cm *db.ChatMeta
	var err error
	if chat != nil {
		ucm := db.MetaFromChat(chat, config.Get().DefaultLanguage)
		cm, err = up.GetChatMeta(chat.ID)
		if err != nil {
			log.WithError(err).WithField("ucm", *ucm).Warn("cant get chat meta")
		}
		if cm != nil && (ucm.Title != cm.Title || ucm.Type != cm.Type) {
			cm.Title = ucm.Title
			cm.Type = ucm.Type
			if uErr := up.s.GetDB().UpsertChatMeta(cm); uErr != nil {
				log.WithError(uErr).Warn("cant update chat meta")
			}
		} else if cm == nil {
			if uErr := up.s.GetDB().UpsertChatMeta(cm); uErr != nil {
				log.WithError(uErr).Warn("cant insert chat meta")
			}
			cm = ucm
		}
		if len(cm.Settings) == 0 {
			cm.Settings = db.ChatSettings{}
			cm.Settings = *db.DefaultChatSettings
			cm.Settings["language"] = config.Get().DefaultLanguage
			if uErr := up.s.GetDB().UpsertChatMeta(cm); uErr != nil {
				log.WithError(uErr).Warn("cant update chat meta settings")
			}
		}
	}

	user := u.SentFrom()
	if user == nil && u.ChatJoinRequest != nil {
		user = &u.ChatJoinRequest.From
	}
	var um *db.UserMeta
	if user != nil {
		uum := db.MetaFromUser(user)
		um, err = up.GetUserMeta(user.ID)
		if err != nil {
			if errors.Cause(err) != sql.ErrNoRows {
				return errors.WithMessage(err, "cant get user meta")
			}
		}
		if um == nil || (uum.FirstName != um.FirstName || uum.LastName != um.LastName || uum.UserName != um.UserName && uum.LanguageCode != um.LanguageCode) {
			if uErr := up.s.GetDB().UpsertUserMeta(uum); uErr != nil {
				log.WithError(uErr).Warn("cant update user meta")
			}
			um = uum
		}
	}

	// Commented out because of the lack of consumers, memory leak
	// TODO: implement event bus based message processing
	// event.Bus.NQ(&UpdateEvent{
	// 	Base:    event.CreateBase("api_update", time.Now().Add(time.Minute)),
	// 	payload: u,
	// })

	for _, handler := range up.updateHandlers {
		proceed, err := handler.Handle(u, cm, um)
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

func (up *UpdateProcessor) GetChatMeta(chatID int64) (cm *db.ChatMeta, err error) {
	defer tool.Catch(func(catch error) {
		err = catch
	})
	r := reg.Get()
	if cm := r.GetCM(chatID); cm != nil {
		return cm, nil
	}
	cm, err = up.s.GetDB().GetChatMeta(chatID)
	tool.Must(err)
	r.SetCM(cm)
	return cm, nil
}

func (up *UpdateProcessor) GetUserMeta(userID int64) (um *db.UserMeta, err error) {
	defer tool.Catch(func(catch error) {
		err = catch
	})
	r := reg.Get()
	if cm := r.GetUM(userID); cm != nil {
		return cm, nil
	}
	um, err = up.s.GetDB().GetUserMeta(userID)
	tool.Must(err)
	return um, nil
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
			ChatID: chatID,
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
			ChatID: chatID,
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
			ChatID: chatID,
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
