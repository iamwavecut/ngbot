package handlers

import (
	"fmt"
	"slices"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
)

var flaggedEmojis = []string{"💩"}


type Reactor struct {
	s bot.Service
}

func NewReactor(s bot.Service) *Reactor {
	r := &Reactor{
		s: s,
	}
	return r
}

func (r *Reactor) Handle(u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := r.getLogEntry()
	if chat == nil || user == nil {
		entry.Debug("no chat or user", u)
		return true, nil
	}

	reaction := u.MessageReaction
	if reaction == nil {
		return true, nil
	}
	b := r.s.GetBot()
	for _, react := range reaction.NewReaction {
		flags := map[string]int{}
		emoji := react.Emoji
		if react.Type == api.StickerTypeCustomEmoji {
			emojiStickers, err := b.GetCustomEmojiStickers(api.GetCustomEmojiStickersConfig{
				CustomEmojiIDs: []string{react.CustomEmoji},
			})
			if err != nil {
				entry.Warn("custom emogi get error", err)
				continue
			}
			emoji = emojiStickers[0].Emoji

		}
		if slices.Contains(flaggedEmojis, react.Emoji) {
			flags[emoji]++
		}

		for _, flagged := range flags {
			if flagged >= 5 {
				if err := bot.BanUserFromChat(b, user.ID, chat.ID); err != nil {
					entry.Error("cant ban user in chat", bot.GetFullName(user), chat.Title)
				}
			}
		}
	}

	if err := r.handleFirstMessage(u, chat, user); err != nil {
		entry.WithError(err).Error("error handlimg new message")
	}

	return true, nil
}

func (r *Reactor) handleFirstMessage(u *api.Update, chat *api.Chat, user *api.User) error {
	b := r.s.GetBot()
	entry := r.getLogEntry()
	if u.FromChat() == nil {
		return nil
	}
	if u.SentFrom() == nil {
		return nil
	}
	m := u.Message
	if m == nil {
		return nil
	}

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

	if err := bot.DeleteChatMessage(b, chat.ID, m.MessageID); err != nil {
		entry.WithError(err).Error("cant delete first message")
	}
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
		return errors.WithMessage(err, "cant restrict")
	}

	lang := r.getLanguage(chat, user)
	nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, bot.GetFullName(user)), user.ID)
	msgText := fmt.Sprintf(i18n.Get("Hi %s! Your first message should be text-only and without any links or media. Just a heads up - if you don't follow this rule, you'll get banned from the group. Cheers!", lang), nameString)
	msg := api.NewMessage(chat.ID, msgText)
	msg.ParseMode = api.ModeMarkdown
	msg.DisableNotification = true
	reply, err := b.Send(msg)
	if err != nil {
		return errors.WithMessage(err, "cant send")
	}
	go func() {
		time.Sleep(30 * time.Second)
		if err := bot.DeleteChatMessage(b, chat.ID, reply.MessageID); err != nil {
			entry.WithError(err).Error("cant delete message")
		}
	}()

	return nil
}

func (r *Reactor) getLogEntry() *log.Entry {
	return log.WithField("context", "admin")
}

func (r *Reactor) getLanguage(chat *api.Chat, user *api.User) string {
	if settings, err := r.s.GetDB().GetSettings(chat.ID); !tool.Try(err) {
		return settings.Language
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		return user.LanguageCode
	}
	return config.Get().DefaultLanguage
}
