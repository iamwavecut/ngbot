package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"strings"

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

func (r *Reactor) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
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
	settings, err := r.s.GetSettings(chat.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			entry.Info("No settings found for chat, creating default settings")
			settings = &db.Settings{
				Enabled:          true,
				ChallengeTimeout: defaultChallengeTimeout,
				RejectTimeout:    defaultRejectTimeout,
				Language:         "en",
				ID:               chat.ID,
			}
			err = r.s.SetSettings(settings)
			if err != nil {
				entry.WithError(err).Error("Failed to set default chat settings")
				return false, fmt.Errorf("failed to set default chat settings: %w", err)
			}
		} else {
			entry.WithError(err).Error("Failed to get chat settings")
			return false, fmt.Errorf("failed to get chat settings: %w", err)
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
		if err := r.handleFirstMessage(ctx, u, chat, user); err != nil {
			entry.WithError(err).Error("error handling new message")
		}
	}

	return true, nil
}

func (r *Reactor) handleFirstMessage(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) error {
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
	isMember, err := r.s.IsMember(ctx, chat.ID, user.ID)
	if err != nil {
		return errors.WithMessage(err, "cant check if member")
	}
	if isMember {
		entry.Debug("user is already a member")
		return nil
	}
	// entry.Debug("checking media in message")
	// if err := r.checkMedia(chat, user, m); err != nil {
	// 	return errors.WithMessage(err, "cant check media")
	// }
	entry.Debug("checking first message content")
	if err := r.checkFirstMessage(ctx, chat, user, m); err != nil {
		return errors.WithMessage(err, "cant check first message")
	}

	return nil
}

func (r *Reactor) checkFirstMessage(ctx context.Context, chat *api.Chat, user *api.User, m *api.Message) error {
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

					Input: Эротика
					Response: NOT_SPAM

					Input: Олегик)))
					Response: NOT_SPAM

					Input: Авантюра!
					Response: NOT_SPAM

					Input: Я всё понял, спасибо!
					Response: NOT_SPAM

					Input: Это не так
					Response: NOT_SPAM

					Input: Не сочтите за спам, хочу порек��амировать свой канал
					Response: NOT_SPAM

					Input: Нет
					Response: NOT_SPAM

					Input: Я всё понял, спасибо!
					Response: NOT_SPAM

					Input: ???
					Response: NOT_SPAM

					Input: ...
					Response: NOT_SPAM

					Input: Да
					Response: NOT_SPAM

					Input: Ищу людей, возьму 2-3 человека 18+ Удаленная деятельность.От 250$  в  день.Кому интересно: Пишите + в лс
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
		var errs []error
		if err := bot.DeleteChatMessage(b, chat.ID, m.MessageID); err != nil {
			errs = append(errs, errors.Wrap(err, "failed to delete message"))
		}
		if err := bot.BanUserFromChat(b, user.ID, chat.ID); err != nil {
			errs = append(errs, errors.Wrap(err, "failed to ban user"))
		}
		lang := r.getLanguage(chat, user)

		if len(errs) > 0 {
			entry.WithField("errors", errs).Error("failed to handle spam")
			var msgContent string
			if len(errs) == 2 {
				msgContent = fmt.Sprintf(i18n.Get("I can't delete messages or ban spammer \"%s\".", lang), bot.GetUN(user))
			} else if errors.Is(errs[0], errors.New("failed to delete message")) {
				msgContent = fmt.Sprintf(i18n.Get("I can't delete messages from spammer \"%s\".", lang), bot.GetUN(user))
			} else {
				msgContent = fmt.Sprintf(i18n.Get("I can't ban spammer \"%s\".", lang), bot.GetUN(user))
			}
			msgContent += " " + i18n.Get("I should have the permissions to ban and delete messages here.", lang)
			msg := api.NewMessage(chat.ID, msgContent)
			msg.ParseMode = api.ModeHTML
			if _, err := b.Send(msg); err != nil {
				entry.WithError(err).Error("failed to send message about lack of permissions")
			}
			return errors.New("failed to handle spam")
		}
		return nil
	}

	entry.Debug("message passed spam check, inserting member")
	if err := r.s.InsertMember(ctx, chat.ID, user.ID); err != nil {
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
