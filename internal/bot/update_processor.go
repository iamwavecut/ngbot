package bot

import (
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
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
	chat, err := GetChat(u)
	if err != nil {
		log.WithError(err).WithField("update", *u).Warn("cant get chat from update")
	}
	var cm *db.ChatMeta
	if chat != nil {
		ucm := db.MetaFromChat(chat, config.Get().DefaultLanguage)
		cm, err := up.GetChatMeta(chat.ID)
		if err != nil {
			log.WithError(err).WithField("update", *u).Warn("cant get chat meta")
			cm = ucm
		}
		if ucm.Title != cm.Title || ucm.Type != cm.Type {
			cm.Title = ucm.Title
			cm.Type = ucm.Type
			if uErr := up.s.GetDB().UpsertChatMeta(cm); uErr != nil {
				log.WithError(uErr).Warn("cant update chat meta")
			}
		}
	}

	user, err := GetUser(u)
	if err != nil {
		log.WithError(err).WithField("update", *u).Warn("cant get user from update")
	}
	var um *db.UserMeta
	if user != nil {
		uum := db.MetaFromUser(user)
		um, err := up.GetUserMeta(user.ID)
		if err != nil {
			if errors.Cause(err) != sqlite.ErrNoUser {
				return errors.WithMessage(err, "cant get user meta")
			}
			um = uum
		}
		if uum != um {
			if uErr := up.s.GetDB().UpsertUserMeta(uum); uErr != nil {
				log.WithError(uErr).Warn("cant update user meta")
			}
			um = uum
		}
	}

	event.Bus.Enqueue(&UpdateEvent{
		Base:    event.CreateBase("api_update", time.Now().Add(time.Minute)),
		payload: u,
	})

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

func GetChat(u *api.Update) (*api.Chat, error) {
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
	r := reg.Get()
	if cm := r.GetCM(chatID); cm != nil {
		return cm, nil
	}
	cm, err := up.s.GetDB().GetChatMeta(chatID)
	if err != nil {
		log.WithError(err).Warn("cant get chat meta")
	}
	r.SetCM(cm)
	return cm, nil
}

func GetUser(u *api.Update) (*api.User, error) {
	if u == nil {
		return nil, errors.New("nil update")
	}
	switch {
	case u.CallbackQuery != nil && u.CallbackQuery.From != nil:
		return u.CallbackQuery.From, nil
	case u.Message != nil && u.Message.From != nil:
		return u.Message.From, nil
	case u.EditedMessage != nil && u.EditedMessage.From != nil:
		return u.EditedMessage.From, nil
	case u.ChosenInlineResult != nil && u.ChosenInlineResult.From != nil:
		return u.ChosenInlineResult.From, nil
	case u.ChannelPost != nil && u.ChannelPost.From != nil:
		return u.ChannelPost.From, nil
	case u.EditedChannelPost != nil && u.EditedChannelPost.From != nil:
		return u.EditedChannelPost.From, nil
	case u.InlineQuery != nil && u.InlineQuery.From != nil:
		return u.InlineQuery.From, nil
	case u.PreCheckoutQuery != nil && u.PreCheckoutQuery.From != nil:
		return u.PreCheckoutQuery.From, nil
	case u.ShippingQuery != nil && u.ShippingQuery.From != nil:
		return u.ShippingQuery.From, nil
	}
	return nil, errors.New("no user")
}

func (up *UpdateProcessor) GetUserMeta(userID int64) (*db.UserMeta, error) {
	r := reg.Get()
	if cm := r.GetUM(userID); cm != nil {
		return cm, nil
	}
	um, err := up.s.GetDB().GetUserMeta(userID)
	if err != nil {
		if errors.Cause(err) != sqlite.ErrNoUser {
			log.WithError(err).Warn("cant get user meta")
		}
		return nil, err
	}

	return um, nil
}

func DeleteChatMessage(bot *api.BotAPI, chatID int64, messageID int) error {
	if _, err := bot.Request(api.NewDeleteMessage(chatID, messageID)); err != nil {
		return err
	}
	return nil
}

func KickUserFromChat(bot *api.BotAPI, userID int64, chatID int64) error {
	if _, err := bot.Request(api.KickChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatID: chatID,
			UserID: userID,
		},
		UntilDate: time.Now().Add(10 * time.Minute).Unix(),
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
			CanSendMediaMessages:  false,
			CanSendPolls:          false,
			CanSendOtherMessages:  false,
			CanAddWebPagePreviews: false,
			CanChangeInfo:         false,
			CanInviteUsers:        false,
			CanPinMessages:        false,
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
			CanSendMediaMessages:  true,
			CanSendPolls:          true,
			CanSendOtherMessages:  true,
			CanAddWebPagePreviews: true,
			CanChangeInfo:         true,
			CanInviteUsers:        true,
			CanPinMessages:        true,
		},
	}); err != nil {
		return errors.WithMessage(err, "cant unrestrict")
	}
	return nil
}

func EscapeMarkdown(md string) string {
	md = strings.Replace(md, "_", "\\_", -1)
	md = strings.Replace(md, "*", "\\*", -1)
	md = strings.Replace(md, "[", "\\[", -1)
	md = strings.Replace(md, "]", "\\]", -1)
	return strings.Replace(md, "`", "\\`", -1)
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
