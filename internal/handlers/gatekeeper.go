package handlers

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/resources"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pborman/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
)

const (
	captchaSize = 5
)

type challengedUser struct {
	user               *api.User
	successFunc        func()
	joinMessageID      int
	challengeMessageID int
	successUUID        string
	targetChat         *api.Chat
	commChat           *api.Chat
}

type Gatekeeper struct {
	s                bot.Service
	joiners          map[int64]map[int64]*challengedUser
	welcomeMessageID int
	newcomers        map[int64]map[int64]struct{}
	restricted       map[int64]map[int64]struct{}

	Variants map[string]map[string]string `yaml:"variants"`
}

var challengeKeys = []string{
	"Hello, %s! We want to be sure you're not a bot, so please select %s. If not, we might have to say goodbye. Thanks for understanding!",
	"Hey %s! To keep this group human-only, could you please choose %s? If you don't, we'll have to say bye-bye. Thanks for your cooperation!",
	"Greetings, %s! We're just checking to make sure you're not a robot. Can you please pick %s? If not, we'll have to let you go. Thanks for your cooperation!",
	"Hi there, %s! We like having humans in this group, so please select %s to prove you're not a bot. If you can't, we might have to remove you. Thanks in advance!",
	"Welcome, %s! We need your help to keep this group human-only. Could you please select %s? If you can't, we might have to remove you. Thanks for your understanding!",
}

var privateChallengeKeys = []string{
	"Hey %s! Exciting to see you're interested in joining the group \"%s\"! We just need one more thing from you to confirm that you're human - pick %s. If you can't, we might have to say goodbye. Thanks for your cooperation!",
	"Hello there, %s! We're happy you want to join \"%s\"! We just need you to pick %s to prove that you're human. If you can't, we might have to remove you. Thanks for your understanding!",
	"Hi %s! We're thrilled you want to be part of \"%s\"! Just one more thing to make sure you're not a bot - please select %s. If you can't, we might have to say goodbye. Thanks for your cooperation!",
	"Welcome, %s! We're glad you're interested in joining \"%s\"! Please pick %s to prove that you're a human being. If you can't, we might have to remove you. Thanks for understanding!",
	"Hey %s! We're excited you want to join the group \"%s\"! Just need a quick test to make sure you're not a robot - pick %s. If you can't, we might have to say bye-bye. Thanks for your cooperation!",
	"Hi there, %s! Joining \"%s\" is fantastic news! Please pick %s to prove you're human. If you can't, we might have to let you go. Thanks for your cooperation!",
	"Hello, %s! We're glad you want to be part of \"%s\"! Just one more step - pick %s to confirm that you're not a bot. If you can't, we might have to say goodbye. Thanks for understanding!",
	"Hey %s! We're excited to see you want to join \"%s\"! To make sure you're not a robot, please select %s. If you can't, we might have to say farewell. Thanks for your cooperation!",
	"Greetings, %s! It's great you want to join \"%s\"! Please pick %s to show that you're human. If you can't, we might have to remove you. Thanks for your understanding!",
	"Hi there, %s! Welcome to the group \"%s\"! We need one more thing from you to confirm that you're human - pick %s. If you can't, we might have to let you go. Thanks for your cooperation!",
}

func NewGatekeeper(s bot.Service) *Gatekeeper {
	g := &Gatekeeper{
		s: s,

		joiners:    map[int64]map[int64]*challengedUser{},
		Variants:   map[string]map[string]string{},
		newcomers:  map[int64]map[int64]struct{}{},
		restricted: map[int64]map[int64]struct{}{},
	}

	entry := g.getLogEntry()
	for _, lang := range i18n.GetLanguagesList() {
		challengesData, err := resources.FS.ReadFile(infra.GetResourcesPath("gatekeeper", "challenges", lang+".yml"))
		if err != nil {
			entry.WithError(err).Errorln("cant load challenges file", lang)
		}

		// entry.Traceln("unmarshal localized challenges", lang)
		localVariants := map[string]string{}
		if err := yaml.Unmarshal(challengesData, &localVariants); err != nil {
			entry.WithError(err).Errorln("cant unmarshal challenges yaml", lang)
		}
		g.Variants[lang] = localVariants
	}
	return g
}

func (g *Gatekeeper) Handle(u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	if chat == nil || user == nil {
		return true, nil
	}
	entry := g.getLogEntry()
	b := g.s.GetBot()
	m := u.Message

	var isFirstMessage bool
	if m != nil && g.newcomers[chat.ID] != nil && m.NewChatMembers != nil {
		_, isFirstMessage = g.newcomers[chat.ID][user.ID]
	}

	switch {
	case u.CallbackQuery != nil:
		entry.Traceln("handle challenge")
		return false, g.handleChallenge(u, chat, user)
	case u.ChatJoinRequest != nil:
		entry.Traceln("handle chat join request")
		return true, g.handleChatJoinRequest(u)
	case m != nil && m.NewChatMembers != nil:
		entry.Traceln("handle new chat members")
		chatInfo, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
			ChatConfig: api.ChatConfig{
				ChatID: m.Chat.ID,
			},
		})
		if err != nil {
			return true, err
		}
		if !chatInfo.JoinByRequest {
			return true, g.handleNewChatMembers(u, chat)
		}
		entry.Traceln("ignoring invited and approved joins")
	case m != nil && m.From.ID == b.Self.ID && m.LeftChatMember != nil:
		return true, bot.DeleteChatMessage(b, chat.ID, m.MessageID)
	case isFirstMessage:
		return true, g.handleFirstMessage(u, chat, user)
	}
	return true, nil
}

func (g *Gatekeeper) handleChallenge(u *api.Update, chat *api.Chat, user *api.User) (err error) {
	entry := g.getLogEntry()
	b := g.s.GetBot()

	cq := u.CallbackQuery
	entry.Traceln(cq.Data, bot.GetUN(user))

	joinerID, challengeUUID, err := func(s string) (int64, string, error) {
		parts := strings.Split(s, ";")
		if len(parts) != 2 {
			return 0, "", errors.New("invalid string to split")
		}
		ID, err := strconv.ParseInt(parts[0], 10, 0)
		if err != nil {
			return 0, "", errors.WithMessage(err, "cant parse user ID")
		}

		return ID, parts[1], nil
	}(cq.Data)
	if err != nil {
		return err
	}
	var isAdmin bool

	if chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatID: chat.ID,
			UserID: user.ID,
		},
	}); err == nil {
		user = chatMember.User
		switch {
		case
			chatMember.IsCreator(),
			chatMember.IsAdministrator() && chatMember.CanRestrictMembers:
			isAdmin = true
		}
	} else {
		return errors.New("cant get chat member")
	}

	lang := g.getLanguage(chat, user)
	cu := g.extractChallengedUser(joinerID, chat.ID)
	if cu == nil {
		entry.Debug("no user matched for challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("This challenge isn't your concern", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}
	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: cu.targetChat.ID,
		},
	})
	if err != nil {
		return errors.WithMessage(err, "cant get target chat info")
	}
	lang = g.getLanguage(&targetChat, user)

	isPublic := cu.commChat.ID == cu.targetChat.ID
	if !isPublic {
		isAdmin = false
	} else {
		_ = bot.RestrictChatting(b, user.ID, cu.targetChat.ID)
	}

	if !isAdmin && joinerID != user.ID {
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}

	switch {
	case isAdmin, cu.successUUID == challengeUUID:
		entry.Debug("successful challenge for ", bot.GetUN(cu.user))
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		if _, err = b.Request(api.NewDeleteMessage(cu.commChat.ID, cu.challengeMessageID)); err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		if !isPublic {
			_ = bot.ApproveJoinRequest(b, cu.user.ID, cu.targetChat.ID)
			msg := api.NewMessage(cu.commChat.ID, fmt.Sprintf(i18n.Get("Awesome, you're good to go! Feel free to start chatting in the group \"%s\".", lang), api.EscapeText(api.ModeMarkdown, cu.targetChat.Title)))
			msg.ParseMode = api.ModeMarkdown
			_ = tool.Err(b.Send(msg))
		} else {
			_ = bot.UnrestrictChatting(b, user.ID, cu.targetChat.ID)
		}

		if cu.successFunc != nil {
			cu.successFunc()
		}
		if _, ok := g.newcomers[cu.targetChat.ID]; !ok {
			g.newcomers[cu.targetChat.ID] = map[int64]struct{}{}
		}
		if _, ok := g.newcomers[cu.targetChat.ID][cu.user.ID]; !ok {
			g.newcomers[cu.targetChat.ID][cu.user.ID] = struct{}{}
		}
		if _, ok := g.restricted[cu.targetChat.ID]; !ok {
			g.restricted[cu.targetChat.ID] = map[int64]struct{}{}
		}
		g.removeChallengedUser(cu.user.ID, cu.commChat.ID)

	case cu.successUUID != challengeUUID:
		entry.Debug("failed challenge for ", bot.GetUN(cu.user))

		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, fmt.Sprintf(i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", lang), cu.targetChat.Title, 10))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		if !isPublic {
			_ = bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChat.ID)
			msg := api.NewMessage(cu.commChat.ID, fmt.Sprintf(i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", lang), cu.targetChat.Title, 10))
			msg.ParseMode = api.ModeMarkdown
			_ = tool.Err(b.Send(msg))
		}
		if cu.joinMessageID != 0 {
			if err := bot.DeleteChatMessage(b, cu.targetChat.ID, cu.joinMessageID); err != nil {
				entry.WithError(err).Error("cant delete join message")
			}
		}

		if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.challengeMessageID); err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChat.ID); err != nil {
			entry.WithError(err).Errorln("cant kick failed")
		}

		// stop timer anyway
		if cu.successFunc != nil {
			cu.successFunc()
		}
		g.removeChallengedUser(cu.user.ID, cu.commChat.ID)
	default:
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("I have no idea what is going on", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
	}
	return err
}

func (g *Gatekeeper) handleNewChatMembers(u *api.Update, chat *api.Chat) error {
	return g.handleJoin(u, u.Message.NewChatMembers, chat, chat)
}

func (g *Gatekeeper) handleChatJoinRequest(u *api.Update) error {
	target := &u.ChatJoinRequest.Chat

	comm, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	})
	if err != nil {
		return err
	}

	return g.handleJoin(u, []api.User{u.ChatJoinRequest.From}, target, &comm)
}

func (g *Gatekeeper) handleJoin(u *api.Update, jus []api.User, target *api.Chat, comm *api.Chat) (err error) {
	if target == nil || comm == nil {
		return errors.New("target or comm is nil")
	}
	entry := g.getLogEntry()
	b := g.s.GetBot()

	for _, ju := range jus {
		if ju.IsBot {
			continue
		}

		if _, err := b.Request(api.RestrictChatMemberConfig{
			ChatMemberConfig: api.ChatMemberConfig{
				ChatID: target.ID,
				UserID: ju.ID,
			},
			UntilDate: time.Now().Add(3 * time.Minute).Unix(),
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
			entry.WithError(err).Error("cant restrict")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)

		cu := &challengedUser{
			user:        &ju,
			successFunc: cancel,
			successUUID: uuid.New(),
			targetChat:  target,
			commChat:    comm,
		}
		if u.Message != nil {
			cu.joinMessageID = u.Message.MessageID
		}
		if _, ok := g.joiners[cu.commChat.ID]; !ok {
			g.joiners[cu.commChat.ID] = map[int64]*challengedUser{}
		}
		g.joiners[cu.commChat.ID][cu.user.ID] = cu
		commLang := g.getLanguage(cu.commChat, cu.user)

		go func() {
			entry.Traceln("setting timer for ", bot.GetUN(cu.user))
			timeout := time.NewTimer(3 * time.Minute)
			defer delete(g.joiners[cu.commChat.ID], cu.user.ID)

			select {
			case <-ctx.Done():
				entry.Info("removing challenge timer for ", bot.GetUN(cu.user))
				timeout.Stop()
				return

			case <-timeout.C:
				entry.Info("challenge timed out for ", bot.GetUN(cu.user))
				if cu.challengeMessageID != 0 {
					if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.challengeMessageID); err != nil {
						entry.WithError(err).Error("cant delete challenge message")
					}
				}
				if cu.joinMessageID != 0 {
					if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.joinMessageID); err != nil {
						entry.WithError(err).Error("cant delete join message")
					}
				}
				if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChat.ID); err != nil {
					entry.WithError(err).Errorln("cant ban ", bot.GetUN(cu.user))
				}
				if cu.commChat.ID != cu.targetChat.ID {
					if err := bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChat.ID); err != nil {
						entry.Traceln("decline failed", err)
					}
					sentMsg, err := b.Send(api.NewMessage(cu.commChat.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", commLang)))
					if err != nil {
						entry.WithError(err).Errorln("cant answer callback query")
					}
					time.AfterFunc(10*time.Minute, func() {
						time.Sleep(10 * time.Minute)
						_ = bot.DeleteChatMessage(b, cu.commChat.ID, sentMsg.MessageID)
					})
				}
				return
			}
		}()

		buttons, correctVariant := g.createCaptchaButtons(cu, commLang)

		var keys []string
		isPublic := cu.commChat.ID == cu.targetChat.ID
		if isPublic {
			keys = challengeKeys
		} else {
			keys = privateChallengeKeys
		}

		randomKey := keys[tool.RandInt(0, len(keys)-1)]
		nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, bot.GetFullName(cu.user)), cu.user.ID)

		args := []interface{}{nameString}
		if !isPublic {
			args = append(args, api.EscapeText(api.ModeMarkdown, target.Title))
		}
		args = append(args, correctVariant[1])
		msgText := fmt.Sprintf(i18n.Get(randomKey, commLang), args...)
		msg := api.NewMessage(cu.commChat.ID, msgText)
		msg.ParseMode = api.ModeMarkdown
		if isPublic {
			msg.DisableNotification = true
		}

		kb := api.NewInlineKeyboardMarkup(
			api.NewInlineKeyboardRow(buttons...),
		)
		msg.ReplyMarkup = kb
		sentMsg, err := b.Send(msg)
		if err != nil {
			return errors.WithMessage(err, "cant send")
		}
		cu.challengeMessageID = sentMsg.MessageID
	}
	return nil
}

func (g *Gatekeeper) handleFirstMessage(u *api.Update, chat *api.Chat, user *api.User) error {
	b := g.s.GetBot()
	entry := g.getLogEntry()
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

	// TODO: implement static ban list as a separate module
	if m.Text != "" {
		for _, v := range []string{"Нужны сотрудников", "Без опыта роботы", "@dasha234di", "@marinetthr", "На момент стажировке", "5 р\\час"} {
			if strings.Contains(m.Text, v) {
				if err := bot.DeleteChatMessage(b, m.Chat.ID, m.MessageID); err != nil {
					entry.WithError(err).Error("cant delete message")
				}
				if err := bot.BanUserFromChat(b, m.From.ID, m.Chat.ID); err != nil {
					entry.WithField("user", bot.GetUN(user)).Info("banned by static list")
					return errors.WithMessage(err, "cant kick")
				}
				return nil
			}
		}
	}

	toRestrict := true
	switch {
	// case m.ForwardFrom != nil:
	// 	entry = entry.WithField("message_type", "forward")
	// case m.ForwardFromChat != nil:
	// 	entry = entry.WithField("message_type", "forward_chat")
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
	default:
		toRestrict = false

		// if len(m.Entities) == 0 {
		// 	toRestrict = false
		// 	break
		// }
		// for _, e := range m.Entities {
		// 	switch e.Type {
		// 	case "url", "text_link":
		// 		entry = entry.WithField("message_type", "with link")
		// 	case "email", "phone_number", "mention", "text_mention":
		// 		entry = entry.WithField("message_type", "with mention")
		// 	}
		// }
	}

	if !toRestrict {
		delete(g.restricted[chat.ID], user.ID)
		delete(g.newcomers[chat.ID], user.ID)
		return nil
	}
	entry.Debug("restricting user")
	if err := bot.DeleteChatMessage(b, chat.ID, m.MessageID); err != nil {
		entry.WithError(err).Error("cant delete first message")
	}
	if _, err := b.Request(api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatID: chat.ID,
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

	_, secondViolation := g.restricted[chat.ID][user.ID]
	if secondViolation {
		entry.Debug("kicking user after second violation")
		err := bot.BanUserFromChat(b, user.ID, chat.ID)
		if err != nil {
			return errors.WithMessage(err, "cant kick")
		}
		delete(g.restricted[chat.ID], user.ID)
		delete(g.newcomers[chat.ID], user.ID)
		return nil
	}
	entry.Debug("first message")

	g.restricted[chat.ID][user.ID] = struct{}{}
	lang := g.getLanguage(chat, user)
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

func (g *Gatekeeper) extractChallengedUser(userID int64, chatID int64) *challengedUser {
	joiner := g.findChallengedUser(userID, chatID)
	if joiner == nil {
		return nil
	}
	delete(g.joiners[chatID], userID)
	return joiner
}

func (g *Gatekeeper) findChallengedUser(userID int64, chatID int64) *challengedUser {
	if _, ok := g.joiners[chatID]; !ok {
		g.getLogEntry().Warnln("no challenges for chat", chatID)
		return nil
	}
	if user, ok := g.joiners[chatID][userID]; ok {
		return user
	}

	g.getLogEntry().Warnln("no challenges for chat user", chatID, userID)
	return nil
}

func (g *Gatekeeper) removeChallengedUser(userID int64, chatID int64) {
	if _, ok := g.joiners[chatID]; !ok {
		g.getLogEntry().Traceln("no challenges for chat", chatID)
		return
	}
	if _, ok := g.joiners[chatID][userID]; ok {
		delete(g.joiners[chatID], userID)
		return
	}
	g.getLogEntry().Traceln("no challenges for chat user", chatID, userID)
}

func (g *Gatekeeper) createCaptchaIndex(lang string) [][2]string {
	vars := g.Variants[lang]
	captchaIndex := make([][2]string, len(vars))
	idx := 0
	for k, v := range vars {
		captchaIndex[idx] = [2]string{k, v}
		idx++
	}
	return captchaIndex
}

func (g *Gatekeeper) createCaptchaButtons(cu *challengedUser, lang string) ([]api.InlineKeyboardButton, [2]string) {
	captchaIndex := g.createCaptchaIndex(lang)
	captchaRandomSet := make([][2]string, 0, captchaSize)
	usedIDs := make(map[int]struct{}, captchaSize)
	for len(captchaRandomSet) < captchaSize {
		ID := rand.Intn(len(captchaIndex))
		if _, ok := usedIDs[ID]; ok {
			continue
		}
		captchaRandomSet = append(captchaRandomSet, captchaIndex[ID])
		usedIDs[ID] = struct{}{}
	}
	correctVariant := captchaRandomSet[rand.Intn(captchaSize-1)+1]
	var buttons []api.InlineKeyboardButton
	for _, v := range captchaRandomSet {
		result := strconv.FormatInt(cu.user.ID, 10) + ";" + uuid.New()
		if v[0] == correctVariant[0] {
			result = strconv.FormatInt(cu.user.ID, 10) + ";" + cu.successUUID
		}
		buttons = append(buttons, api.NewInlineKeyboardButtonData(v[0], result))
	}
	return buttons, correctVariant
}

func (g *Gatekeeper) getLogEntry() *log.Entry {
	return log.WithField("context", "gatekeeper")
}

func (g *Gatekeeper) getLanguage(chat *api.Chat, user *api.User) string {
	if lang, err := g.s.GetDB().GetChatLanguage(chat.ID); !tool.Try(err) {
		return lang
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		return user.LanguageCode
	}
	return config.Get().DefaultLanguage
}
