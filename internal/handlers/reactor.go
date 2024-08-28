package handlers

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	"github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
)

var flaggedEmojis = []string{"💩", "👎", "🖕", "🤮", "🤬", "😡", "💀", "☠️", "🤢", "👿"}

type Reactor struct {
	s      bot.Service
	llmAPI *openai.Client
}

func NewReactor(s bot.Service, llmAPI *openai.Client, model string) *Reactor {
	log.WithField("scope", "Reactor").WithField("method", "NewReactor").Debug("creating new Reactor")
	r := &Reactor{
		s:      s,
		llmAPI: llmAPI,
	}
	return r
}

func (r *Reactor) Handle(u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry().WithField("method", "Handle")
	entry.Debug("handling update")

	nonNilFields := []string{}
	isNonNilPtr := func(v reflect.Value) bool {
		return v.Kind() == reflect.Ptr && !v.IsNil()
	}
	val := reflect.ValueOf(u).Elem()
	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldName := typ.Field(i).Name

		if isNonNilPtr(field) {
			nonNilFields = append(nonNilFields, fieldName)
		}
	}
	entry.Debug("Checking update type")
	if u.Message == nil && u.MessageReaction == nil {
		entry.Debug("Update is not about message or reaction, not proceeding")
		return false, nil
	}
	entry.Debug("Update is about message or reaction, proceeding")

	if chat == nil {
		entry.Debug("no chat")
		entry.Debugf("Non-nil fields: %s", strings.Join(nonNilFields, ", "))
		return true, nil
	}
	if user == nil {
		entry.Debug("no user")
		entry.Debugf("Non-nil fields: %s", strings.Join(nonNilFields, ", "))
		return true, nil
	}

	entry.Debug("Fetching chat settings")
	settings, err := r.s.GetDB().GetSettings(chat.ID)
	if err != nil {
		entry.WithError(err).Error("failed to get chat settings")
		entry.Debug("Creating default settings")
		settings = &db.Settings{
			Enabled:          true,
			ChallengeTimeout: defaultChallengeTimeout,
			RejectTimeout:    defaultRejectTimeout,
			Language:         "en",
			ID:               chat.ID,
		}
		err = r.s.GetDB().SetSettings(settings)
		if err != nil {
			entry.WithError(err).Error("failed to set chat settings")
		}
	}
	if !settings.Enabled {
		entry.Debug("reactor is disabled for this chat")
		return true, nil
	}

	b := r.s.GetBot()

	if u.MessageReaction != nil {
		entry.Debug("Processing message reaction")
		for _, react := range u.MessageReaction.NewReaction {
			flags := map[string]int{}
			emoji := react.Emoji
			if react.Type == api.StickerTypeCustomEmoji {
				entry.Debug("processing custom emoji")
				emojiStickers, err := b.GetCustomEmojiStickers(api.GetCustomEmojiStickersConfig{
					CustomEmojiIDs: []string{react.CustomEmoji},
				})
				if err != nil {
					entry.Warn("custom emoji get error", err)
					continue
				}
				emoji = emojiStickers[0].Emoji
			}
			if slices.Contains(flaggedEmojis, emoji) {
				entry.Debug("flagged emoji detected:", emoji)
				flags[emoji]++
			}

			for _, flagged := range flags {
				if flagged >= 5 {
					entry.Debug("user reached flag threshold, attempting to ban")
					if err := bot.BanUserFromChat(b, user.ID, chat.ID); err != nil {
						entry.Error("cant ban user in chat", bot.GetFullName(user), chat.Title)
					}
				}
			}
		}
	}

	if u.Message != nil {
		entry.Debug("handling first message")
		if err := r.handleFirstMessage(u, chat, user); err != nil {
			entry.WithError(err).Error("error handling new message")
		}
	}

	return true, nil
}

func (r *Reactor) handleFirstMessage(u *api.Update, chat *api.Chat, user *api.User) error {
	entry := r.getLogEntry().WithField("method", "handleFirstMessage")
	entry.Debug("handling first message")
	if u.FromChat() == nil {
		entry.Debug("no chat in update")
		return nil
	}
	if u.SentFrom() == nil {
		entry.Debug("no sender in update")
		return nil
	}
	m := u.Message
	if m == nil {
		entry.Debug("no message in update")
		return nil
	}

	entry.Debug("checking if user is a member")
	if isMember, err := r.s.GetDB().IsMember(chat.ID, user.ID); err != nil {
		return errors.WithMessage(err, "cant check if member")
	} else if isMember {
		entry.Debug("user is already a member")
		return nil
	}
	// entry.Debug("checking media in message")
	// if err := r.checkMedia(chat, user, m); err != nil {
	// 	return errors.WithMessage(err, "cant check media")
	// }
	entry.Debug("checking first message content")
	if err := r.checkFirstMessage(chat, user, m); err != nil {
		return errors.WithMessage(err, "cant check first message")
	}

	return nil
}

func (r *Reactor) checkFirstMessage(chat *api.Chat, user *api.User, m *api.Message) error {
	entry := r.getLogEntry().WithField("method", "checkFirstMessage")
	entry.Debug("checking first message")
	b := r.s.GetBot()

	messageContent := m.Text
	if messageContent == "" && m.Caption != "" {
		messageContent = m.Caption
	}

	if messageContent == "" {
		entry.Warn("empty message content, skipping spam check")
		return nil
	}

	ctx := context.Background()
	entry.Debug("sending message to OpenAI for spam check")
	resp, err := r.llmAPI.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: "openai/gpt-4o-mini",
			Messages: []openai.ChatCompletionMessage{
				{
					Role: openai.ChatMessageRoleSystem,
					Content: `You are a spam detection system.
					Respond with 'SPAM' if the message is spam, or 'NOT_SPAM' if it's not.
					Provide no other output.
					
					<example>
					Input: Hello, how are you?
					Response: NOT_SPAM

					Input: Хочешь зарабатывать на удалёнке но не знаешь как? Напиши мне и я тебе всё расскажу, от 18 лет. жду всех желающих в лс.
					Response: SPAM

					Input: Нужны люди! Стабильнный доход, каждую неделю, на удалёнке, от 18 лет, пишите в лс.
					Response: SPAM

					Input: Ищу людeй, заинтeрeсованных в хoрoшем доп.доходе на удаленке. Не полная занятость, от 21. По вопросам пишите в ЛС
					Response: SPAM

					Input: 10000х Орууу в других играл и такого не разу не было, просто капец  а такое возможно???? 

🥇Первая игровая платформа в Telegram

https://t.me/jetton?start=cdyrsJsbvYy
					Response: SPAM

					Input: Набираю команду нужно 2-3 человека на удалённую работу з телефона пк от  десят тысяч в день  пишите + в лс
					Response: SPAM

					Input: Набираю команду нужно 2-3 человека на удалённую работу з телефона пк от  десят тысяч в день  пишите + в лс
					Response: SPAM

					Input: 💎 Пᴩᴏᴇᴋᴛ TONCOIN, ʙыᴨуᴄᴛиᴧ ᴄʙᴏᴇᴦᴏ ᴋᴀɜинᴏ бᴏᴛᴀ ʙ ᴛᴇᴧᴇᴦᴩᴀʍʍᴇ

👑 Сᴀʍыᴇ ʙыᴄᴏᴋиᴇ ɯᴀнᴄы ʙыиᴦᴩыɯᴀ 
⏳ Мᴏʍᴇнᴛᴀᴧьный ʙʙᴏд и ʙыʙᴏд
🎲 Нᴇ ᴛᴩᴇбуᴇᴛ ᴩᴇᴦиᴄᴛᴩᴀции
🏆 Вᴄᴇ ᴧучɯиᴇ ᴨᴩᴏʙᴀйдᴇᴩы и иᴦᴩы 

🍋 Зᴀбᴩᴀᴛь 1000 USDT 👇

t.me/slotsTON_BOT?start=cdyoNKvXn75
					Response: SPAM
					</example>
					`,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: messageContent,
				},
			},
		},
	)

	if err != nil {
		entry.WithError(err).Error("failed to create chat completion")
		return errors.Wrap(err, "failed to create chat completion")
	}

	if len(resp.Choices) > 0 && resp.Choices[0].Message.Content == "SPAM" {
		entry.Info("spam detected, banning user")
		if err := bot.BanUserFromChat(b, user.ID, chat.ID); err != nil {

			entry.Info("failed to ban user due to lack of permissions")
			msg := api.NewMessage(chat.ID, fmt.Sprintf("I can't ban spammer \"%s\". I should have the permissions to ban and delete messages here.", bot.GetUN(user)))
			msg.ParseMode = api.ModeMarkdown
			if _, err := b.Send(msg); err != nil {
				entry.WithError(err).Error("failed to send message about lack of permissions")
			}
			entry.WithError(err).Error("failed to ban user")
			return errors.Wrap(err, "failed to ban user")
		}
		return nil
	}

	entry.Debug("message passed spam check, inserting member")
	if err := r.s.GetDB().InsertMember(chat.ID, user.ID); err != nil {
		entry.WithError(err).Error("failed to insert member")
		return errors.Wrap(err, "failed to insert member")
	}

	entry.Info("message passed spam check, user added to members")
	return nil

}

func (r *Reactor) getLogEntry() *log.Entry {
	return log.WithField("object", "Reactor")
}

func (r *Reactor) getLanguage(chat *api.Chat, user *api.User) string {
	entry := r.getLogEntry().WithField("method", "getLanguage")
	entry.Debug("getting language for chat and user")
	if settings, err := r.s.GetDB().GetSettings(chat.ID); !tool.Try(err) {
		entry.Debug("using language from chat settings:", settings.Language)
		return settings.Language
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		entry.Debug("using user's language:", user.LanguageCode)
		return user.LanguageCode
	}
	entry.Debug("using default language:", config.Get().DefaultLanguage)
	return config.Get().DefaultLanguage
}

func (r *Reactor) checkMedia(chat *api.Chat, user *api.User, m *api.Message) error {
	entry := r.getLogEntry().WithField("method", "checkMedia")
	entry.Debug("checking media in message")
	b := r.s.GetBot()

	switch {
	case m.ViaBot != nil:
		entry = entry.WithField("message_type", "via_bot")
	case m.Audio != nil:
		entry = entry.WithField("message_type", "audio")
	case m.Document != nil:
		entry = entry.WithField("message_type", "document")
	case m.Photo != nil:
		entry = entry.WithField("message_type", "photo")
	case m.Video != nil:
		entry = entry.WithField("message_type", "video")
	case m.VideoNote != nil:
		entry = entry.WithField("message_type", "video_note")
	case m.Voice != nil:
		entry = entry.WithField("message_type", "voice")
	}

	entry.Debug("restricting user's permissions")
	if _, err := b.Request(api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
			UserID: user.ID,
		},
		UntilDate: time.Now().Add(1 * time.Minute).Unix(),
		Permissions: &api.ChatPermissions{
			CanSendMessages:       true,
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
		entry.WithError(err).Error("failed to restrict user")
		return errors.WithMessage(err, "cant restrict")
	}

	lang := r.getLanguage(chat, user)
	nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, bot.GetFullName(user)), user.ID)
	msgText := fmt.Sprintf(i18n.Get("Hi %s! Your first message should be text-only and without any links or media. Just a heads up - if you don't follow this rule, you'll get banned from the group. Cheers!", lang), nameString)
	msg := api.NewMessage(chat.ID, msgText)
	msg.ParseMode = api.ModeMarkdown
	msg.DisableNotification = true
	entry.Debug("sending warning message to user")
	reply, err := b.Send(msg)
	if err != nil {
		entry.WithError(err).Error("failed to send warning message")
		return errors.WithMessage(err, "cant send")
	}
	go func() {
		entry.Debug("starting timer to delete warning message")
		time.Sleep(30 * time.Second)
		if err := bot.DeleteChatMessage(b, chat.ID, reply.MessageID); err != nil {
			entry.WithError(err).Error("cant delete message")
		}
	}()
	return nil
}
