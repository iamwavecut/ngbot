package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/config"
)

const (
	UpdateTimeout = 5 * time.Minute
)

type (
	UpdateProcessor struct {
		s              Service
		updateHandlers []Handler
	}

	MessageType string
)

const (
	MessageTypeText              MessageType = "text"
	MessageTypeAnimation         MessageType = "animation"
	MessageTypeAudio             MessageType = "audio"
	MessageTypeContact           MessageType = "contact"
	MessageTypeDice              MessageType = "dice"
	MessageTypeDocument          MessageType = "document"
	MessageTypeGame              MessageType = "game"
	MessageTypeInvoice           MessageType = "invoice"
	MessageTypeLocation          MessageType = "location"
	MessageTypePhoto             MessageType = "photo"
	MessageTypePoll              MessageType = "poll"
	MessageTypeSticker           MessageType = "sticker"
	MessageTypeStory             MessageType = "story"
	MessageTypeVenue             MessageType = "venue"
	MessageTypeVideo             MessageType = "video"
	MessageTypeVideoNote         MessageType = "video_note"
	MessageTypeVoice             MessageType = "voice"
	MessageTypeEditedMessage     MessageType = "edited_message"
	MessageTypeChannelPost       MessageType = "channel_post"
	MessageTypeEditedChannelPost MessageType = "edited_channel_post"
	MessageTypePollAnswer        MessageType = "poll_answer"
	MessageTypeMyChatMember      MessageType = "my_chat_member"
	MessageTypeChatMember        MessageType = "chat_member"
	MessageTypeChatJoinRequest   MessageType = "chat_join_request"
	MessageTypeChatBoost         MessageType = "chat_boost"
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

func (up *UpdateProcessor) Process(ctx context.Context, u *api.Update) error {
	if u == nil {
		return errors.New("update is nil")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		var updateTime time.Time
		switch {
		case u.Message != nil:
			updateTime = time.Unix(int64(u.Message.Date), 0)
		case u.EditedMessage != nil:
			updateTime = time.Unix(int64(u.EditedMessage.Date), 0)
		case u.ChannelPost != nil:
			updateTime = time.Unix(int64(u.ChannelPost.Date), 0)
		case u.EditedChannelPost != nil:
			updateTime = time.Unix(int64(u.EditedChannelPost.Date), 0)
		default:
			updateTime = time.Now()
		}

		if time.Since(updateTime) > UpdateTimeout {
			log.WithFields(log.Fields{
				"update_time": updateTime,
				"age":         time.Since(updateTime),
			}).Debug("Skipping outdated update")
			return nil
		}

		chat := u.FromChat()
		if chat == nil {
			switch {
			case u.ChatJoinRequest != nil:
				chat = &u.ChatJoinRequest.Chat
			case u.MyChatMember != nil:
				chat = &u.MyChatMember.Chat
			case u.ChatMember != nil:
				chat = &u.ChatMember.Chat
			}
		}

		user := u.SentFrom()
		if user == nil {
			switch {
			case u.ChatJoinRequest != nil:
				user = &u.ChatJoinRequest.From
			case u.MyChatMember != nil:
				user = &u.MyChatMember.From
			case u.ChatMember != nil:
				user = &u.ChatMember.From
			}
		}

		for _, handler := range up.updateHandlers {
			if handler == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				proceed, err := handler.Handle(ctx, u, chat, user)
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

func DeleteChatMessage(ctx context.Context, bot *api.BotAPI, chatID int64, messageID int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		if _, err := bot.Request(api.NewDeleteMessage(chatID, messageID)); err != nil {
			return err
		}
		return nil
	}
}

func BanUserFromChat(ctx context.Context, bot *api.BotAPI, userID int64, chatID int64, untilUnix int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		if _, err := bot.Request(api.BanChatMemberConfig{
			ChatMemberConfig: api.ChatMemberConfig{
				ChatConfig: api.ChatConfig{
					ChatID: chatID,
				},
				UserID: userID,
			},
			UntilDate:      untilUnix,
			RevokeMessages: true,
		}); err != nil {
			return errors.WithMessage(err, "cant kick")
		}
		return nil
	}
}

func RestrictChatting(ctx context.Context, bot *api.BotAPI, userID int64, chatID int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
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
}

func UnrestrictChatting(ctx context.Context, bot *api.BotAPI, userID int64, chatID int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
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
}

func ApproveJoinRequest(ctx context.Context, bot *api.BotAPI, userID int64, chatID int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
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
}

func DeclineJoinRequest(ctx context.Context, bot *api.BotAPI, userID int64, chatID int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
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

func ExtractContentFromMessage(msg *api.Message) (content string) {
	var markupContent string
	defer func() {
		content = strings.TrimSpace(content)
		markupContent = strings.TrimSpace(markupContent)
		if markupContent != "" {
			content = strings.TrimSpace(content + " " + markupContent)
		}
	}()

	content = strings.TrimSpace(msg.Text + " " + msg.Caption)

	addMessageType := false
	messageType := GetMessageType(msg)
	switch messageType {
	case MessageTypeAnimation:
		addMessageType = true
	case MessageTypeAudio:
		content += fmt.Sprintf(" [%s] %s", messageType, msg.Audio.Title)
	case MessageTypeContact:
		content += fmt.Sprintf(" [%s] %s", messageType, msg.Contact.PhoneNumber)
	case MessageTypeDice:
		content += fmt.Sprintf(" [%s] %s (%d)", messageType, msg.Dice.Emoji, msg.Dice.Value)
	case MessageTypeDocument:
		addMessageType = true
	case MessageTypeGame:
		content += fmt.Sprintf(" [%s] %s %s", messageType, msg.Game.Title, msg.Game.Description)
	case MessageTypeInvoice:
		content += fmt.Sprintf(" [%s] %s %s", messageType, msg.Invoice.Title, msg.Invoice.Description)
	case MessageTypeLocation:
		content += fmt.Sprintf(" [%s] %f,%f", messageType, msg.Location.Latitude, msg.Location.Longitude)
	case MessageTypePoll:
		content += fmt.Sprintf(" [%s] %s", messageType, msg.Poll.Question)
	case MessageTypeStory:
		addMessageType = true
	case MessageTypeVenue:
		content += fmt.Sprintf(" [%s] %s %s", messageType, msg.Venue.Title, msg.Venue.Address)
	case MessageTypeVideo:
		addMessageType = true
	case MessageTypeVideoNote:
		addMessageType = true
	case MessageTypeVoice:
		addMessageType = true
	}
	if addMessageType {
		content += fmt.Sprintf(" [%s]", messageType)
	}

	if msg.ReplyMarkup != nil {
		var buttonTexts []string
		for _, row := range msg.ReplyMarkup.InlineKeyboard {
			for _, button := range row {
				if button.Text != "" {
					buttonTexts = append(buttonTexts, button.Text)
				}
			}
		}
		if len(buttonTexts) > 0 {
			markupContent = strings.Join(buttonTexts, " ")
		}
	}

	return content
}

func GetMessageType(msg *api.Message) MessageType {
	switch {
	case msg.Animation != nil:
		return MessageTypeAnimation
	case msg.Audio != nil:
		return MessageTypeAudio
	case msg.Contact != nil:
		return MessageTypeContact
	case msg.Dice != nil:
		return MessageTypeDice
	case msg.Document != nil:
		return MessageTypeDocument
	case msg.Game != nil:
		return MessageTypeGame
	case msg.Invoice != nil:
		return MessageTypeInvoice
	case msg.Location != nil:
		return MessageTypeLocation
	case msg.Photo != nil:
		return MessageTypePhoto
	case msg.Poll != nil:
		return MessageTypePoll
	case msg.Sticker != nil:
		return MessageTypeSticker
	case msg.Story != nil:
		return MessageTypeStory
	case msg.Venue != nil:
		return MessageTypeVenue
	case msg.Video != nil:
		return MessageTypeVideo
	case msg.VideoNote != nil:
		return MessageTypeVideoNote
	case msg.Voice != nil:
		return MessageTypeVoice
	default:
		return MessageTypeText
	}
}
