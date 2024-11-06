package bot

import (
	"context"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api/v6"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/config"
)

type (
	Handler interface {
		Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error)
	}

	UpdateProcessor struct {
		s              Service
		updateHandlers []Handler
		ctx            context.Context
		cancel         context.CancelFunc
	}
)

var registeredHandlers = make(map[string]Handler)

func RegisterUpdateHandler(title string, handler Handler) {
	registeredHandlers[title] = handler
}

func NewUpdateProcessor(ctx context.Context, s Service) *UpdateProcessor {
	enabledHandlers := make([]Handler, 0)
	for _, handlerName := range config.Get().EnabledHandlers {
		if _, ok := registeredHandlers[handlerName]; !ok || registeredHandlers[handlerName] == nil {
			log.Warnf("no registered handler: %s", handlerName)
			continue
		}
		enabledHandlers = append(enabledHandlers, registeredHandlers[handlerName])
	}

	ctx, cancel := context.WithCancel(ctx)
	return &UpdateProcessor{
		s:              s,
		updateHandlers: enabledHandlers,
		ctx:            ctx,
		cancel:         cancel,
	}
}

func (up *UpdateProcessor) Process(u *api.Update) error {
	if u == nil {
		return errors.New("update is nil")
	}

	select {
	case <-up.ctx.Done():
		return up.ctx.Err()
	default:
		var updateTime time.Time
		switch {
		case u.Message != nil:
			updateTime = time.Unix(int64(u.Message.Date), 0)
		case u.ChannelPost != nil:
			updateTime = time.Unix(int64(u.ChannelPost.Date), 0)
		default:
			return nil
		}

		if time.Since(updateTime) > time.Minute {
			return nil
		}

		chat := u.FromChat()
		if chat == nil && u.ChatJoinRequest != nil {
			chat = &u.ChatJoinRequest.Chat
		}

		user := u.SentFrom()
		if user == nil && u.ChatJoinRequest != nil {
			user = &u.ChatJoinRequest.From
		}

		for _, handler := range up.updateHandlers {
			if handler == nil {
				continue
			}
			select {
			case <-up.ctx.Done():
				return up.ctx.Err()
			default:
				proceed, err := handler.Handle(up.ctx, u, chat, user)
				if err != nil {
					return errors.WithMessage(err, "handling error")
				}
				if !proceed {
					log.Trace("not proceeding")
					return nil
				}
			}
		}
		return nil
	}
}

func (up *UpdateProcessor) Shutdown() {
	up.cancel()
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
		return errors.WithMessage(err, "cant decline join request")
	}
	return nil
}

func GetUpdatesChans(ctx context.Context, bot *api.BotAPI, config api.UpdateConfig) (api.UpdatesChannel, chan error) {
	ch := make(chan api.Update, bot.Buffer)
	chErr := make(chan error)

	go func() {
		defer close(ch)
		defer close(chErr)
		for {
			select {
			case <-ctx.Done():
				chErr <- ctx.Err()
				return
			default:
				updates, err := bot.GetUpdates(config)
				if err != nil {
					chErr <- err
					return
				}

				for _, update := range updates {
					if update.UpdateID >= config.Offset {
						config.Offset = update.UpdateID + 1
						select {
						case ch <- update:
						case <-ctx.Done():
							chErr <- ctx.Err()
							return
						}
					}
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
	if len(userName) == 0 {
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
	if len(fullName) == 0 {
		fullName = user.UserName
	}
	return fullName
}
