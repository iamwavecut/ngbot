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
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
)

const (
	captchaSize = 5
)

type challengedUser struct {
	user               *db.UserMeta
	successFunc        func()
	name               string
	joinMessageID      int
	challengeMessageID int
	successUUID        string
	targetChatID       int64
	commChatID         int64
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

	for _, lang := range [2]string{"en", "ru"} {
		entry.Traceln("loading localized challenges")
		challengesData, err := resources.FS.ReadFile(infra.GetResourcesPath("gatekeeper", "challenges", lang+".yml"))
		if err != nil {
			entry.WithError(err).Errorln("cant load challenges file")
		}

		entry.Traceln("unmarshal localized challenges")
		localVariants := map[string]string{}
		if err := yaml.Unmarshal(challengesData, &localVariants); err != nil {
			entry.WithError(err).Errorln("cant unmarshal challenges yaml")
		}
		g.Variants[lang] = localVariants
	}
	return g
}

func (g *Gatekeeper) Handle(u *api.Update, cm *db.ChatMeta, um *db.UserMeta) (bool, error) {
	if cm == nil {
		return true, nil
	}
	entry := g.getLogEntry()
	b := g.s.GetBot()
	m := u.Message

	var isFirstMessage bool
	if m != nil && g.newcomers[cm.ID] != nil && m.NewChatMembers != nil {
		_, isFirstMessage = g.newcomers[cm.ID][um.ID]
	}

	switch {
	case u.CallbackQuery != nil:
		entry.Traceln("handle challenge")
		return false, g.handleChallenge(u, cm, um)
	case u.ChatJoinRequest != nil:
		entry.Traceln("handle chat join request")
		return true, g.handleChatJoinRequest(u, um)
	case m != nil && m.NewChatMembers != nil:
		entry.Traceln("handle new chat members")
		chat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
			ChatConfig: api.ChatConfig{
				ChatID: m.Chat.ID,
			},
		})
		if err != nil {
			return true, err
		}
		if !chat.JoinByRequest {
			return true, g.handleNewChatMembers(u, cm)
		}
		entry.Traceln("ignoring invited and approved joins")
	case m != nil && m.From.ID == b.Self.ID && m.LeftChatMember != nil:
		return true, bot.DeleteChatMessage(b, cm.ID, m.MessageID)
	case isFirstMessage:
		return true, g.handleFirstMessage(u, cm, um)
	}
	return true, nil
}

func (g *Gatekeeper) handleChallenge(u *api.Update, cm *db.ChatMeta, um *db.UserMeta) (err error) {
	entry := g.getLogEntry()
	b := g.s.GetBot()

	cq := u.CallbackQuery
	entry.Traceln(cq.Data, um.GetUN())

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
	var user *api.User
	if chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			UserID: um.ID,
			ChatID: cm.ID,
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
	lang := getLanguage(nil, user)
	cu := g.extractChallengedUser(joinerID, cm.ID)
	if cu == nil {
		entry.Debug("no user matched for challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("That challenge isn't yours", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}
	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: cu.targetChatID,
		},
	})
	if err != nil {
		return errors.WithMessage(err, "cant get target chat info")
	}
	lang = getLanguage(&targetChat, user)

	isPublic := cu.commChatID == cu.targetChatID
	if !isPublic {
		isAdmin = false
	} else {
		bot.RestrictChatting(b, user.ID, cu.targetChatID)
	}

	if !isAdmin && joinerID != um.ID {
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}

	switch {
	case isAdmin, cu.successUUID == challengeUUID:
		entry.Debug("successful challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		if _, err = b.Request(api.NewDeleteMessage(cu.commChatID, cu.challengeMessageID)); err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		if !isPublic {
			bot.ApproveJoinRequest(b, cu.user.ID, cu.targetChatID)
			msg := api.NewMessage(cu.commChatID, i18n.Get("Awesome, you're good to go! Feel free to start chatting in the group.", lang))
			msg.ParseMode = api.ModeMarkdown
			b.Send(msg)
		} else {
			bot.UnrestrictChatting(b, user.ID, cu.targetChatID)
		}

		if cu.successFunc != nil {
			cu.successFunc()
		}
		if _, ok := g.newcomers[cu.targetChatID]; !ok {
			g.newcomers[cu.targetChatID] = map[int64]struct{}{}
		}
		if _, ok := g.newcomers[cu.targetChatID][cu.user.ID]; !ok {
			g.newcomers[cu.targetChatID][cu.user.ID] = struct{}{}
		}
		if _, ok := g.restricted[cu.targetChatID]; !ok {
			g.restricted[cu.targetChatID] = map[int64]struct{}{}
		}

	case cu.successUUID != challengeUUID:
		entry.Debug("failed challenge")

		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		if !isPublic {
			bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChatID)
			msg := api.NewMessage(cu.commChatID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", lang))
			msg.ParseMode = api.ModeMarkdown
			b.Send(msg)
		}
		if cu.joinMessageID != 0 {
			if err := bot.DeleteChatMessage(b, cu.targetChatID, cu.joinMessageID); err != nil {
				entry.WithError(err).Error("cant delete join message")
			}
		}

		if err := bot.DeleteChatMessage(b, cu.commChatID, cu.challengeMessageID); err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChatID); err != nil {
			entry.WithError(err).Errorln("cant kick failed")
		}

		// stop timer anyway
		if cu.successFunc != nil {
			cu.successFunc()
		}

	default:
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("I have no idea what is going on", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
	}
	return err
}

func (g *Gatekeeper) handleNewChatMembers(u *api.Update, cm *db.ChatMeta) error {
	return g.handleJoin(u, u.Message.NewChatMembers, cm, cm)
}

func (g *Gatekeeper) handleChatJoinRequest(u *api.Update, um *db.UserMeta) error {
	lang := getLanguage(&u.ChatJoinRequest.Chat, &u.ChatJoinRequest.From)
	target := db.MetaFromChat(&u.ChatJoinRequest.Chat, lang)
	comm := &db.ChatMeta{
		ID:       u.ChatJoinRequest.UserChatID,
		Language: lang,
		Type:     "private",
		Settings: *db.DefaultChatSettings,
	}
	comm.Settings["language"] = lang
	return g.handleJoin(u, []api.User{u.ChatJoinRequest.From}, target, comm)
}

func (g *Gatekeeper) handleJoin(u *api.Update, jus []api.User, target *db.ChatMeta, comm *db.ChatMeta) (err error) {
	entry := g.getLogEntry()
	b := g.s.GetBot()

	for _, ju := range jus {
		jum := db.MetaFromUser(&ju)
		if jum.IsBot {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)

		cu := &challengedUser{
			user:         jum,
			successFunc:  cancel,
			name:         api.EscapeText(api.ModeMarkdown, jum.GetFullName()),
			successUUID:  uuid.New(),
			targetChatID: target.ID,
			commChatID:   comm.ID,
		}
		if u.Message != nil {
			cu.joinMessageID = u.Message.MessageID
		}
		if _, ok := g.joiners[cu.commChatID]; !ok {
			g.joiners[cu.commChatID] = map[int64]*challengedUser{}
		}
		g.joiners[cu.commChatID][jum.ID] = cu

		go func() {
			entry.Traceln("setting timer for", jum.GetUN())
			timeout := time.NewTimer(3 * time.Minute)
			defer delete(g.joiners[cu.commChatID], cu.user.ID)

			select {
			case <-ctx.Done():
				entry.Info("aborting challenge timer")
				timeout.Stop()
				return

			case <-timeout.C:
				entry.Info("challenge timed out")
				if cu.challengeMessageID != 0 {
					if err := bot.DeleteChatMessage(b, cu.commChatID, cu.challengeMessageID); err != nil {
						entry.WithError(err).Error("cant delete challenge message")
					}
				}
				if cu.joinMessageID != 0 {
					if err := bot.DeleteChatMessage(b, cu.commChatID, cu.joinMessageID); err != nil {
						entry.WithError(err).Error("cant delete challenge message")
					}
				}
				if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChatID); err != nil {
					entry.WithError(err).Errorln("cant ban join requester")
				}
				if cu.commChatID != cu.targetChatID {
					if err := bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChatID); err != nil {
						entry.Traceln("decline failed", err)
					}
					sentMsg, err := b.Send(api.NewMessage(cu.commChatID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", getLanguage(&api.Chat{
						ID: cu.commChatID,
					}, &ju))))
					if err != nil {
						entry.WithError(err).Errorln("cant answer callback query")
					}
					time.AfterFunc(10*time.Minute, func() {

						time.Sleep(10 * time.Minute)
						bot.DeleteChatMessage(b, cu.commChatID, sentMsg.MessageID)
					})
				}
				return
			}
		}()

		buttons, correctVariant := g.createCaptchaButtons(cu, comm.Language)

		var keys []string
		isPulic := cu.commChatID == cu.targetChatID
		if isPulic {
			keys = challengeKeys
		} else {
			keys = privateChallengeKeys
		}

		randomKey := keys[tool.RandInt(0, len(keys)-1)]
		nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, cu.user.GetFullName()), cu.user.ID)

		args := []interface{}{nameString}
		if !isPulic {
			args = append(args, target.Title)
		}
		args = append(args, correctVariant[1])
		msgText := fmt.Sprintf(i18n.Get(randomKey, comm.Language), args...)
		msg := api.NewMessage(cu.commChatID, msgText)
		msg.ParseMode = api.ModeMarkdown
		if isPulic {
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

func (g *Gatekeeper) handleFirstMessage(u *api.Update, cm *db.ChatMeta, um *db.UserMeta) error {
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
					entry.WithField("user", um.GetUN()).Info("banned by static list")
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
		delete(g.restricted[cm.ID], um.ID)
		delete(g.newcomers[cm.ID], um.ID)
		return nil
	}
	entry.Debug("restricting user")
	if err := bot.DeleteChatMessage(b, cm.ID, m.MessageID); err != nil {
		entry.WithError(err).Error("cant delete first message")
	}
	if _, err := b.Request(api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{
			ChatID: cm.ID,
			UserID: um.ID,
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

	_, secondViolation := g.restricted[cm.ID][um.ID]
	if secondViolation {
		entry.Debug("kicking user after second violation")
		err := bot.BanUserFromChat(b, um.ID, cm.ID)
		if err != nil {
			return errors.WithMessage(err, "cant kick")
		}
		delete(g.restricted[cm.ID], um.ID)
		delete(g.newcomers[cm.ID], um.ID)
		return nil
	}
	entry.Debug("first message")
	g.restricted[cm.ID][um.ID] = struct{}{}

	nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, um.GetFullName()), um.ID)
	msgText := fmt.Sprintf(i18n.Get("Hi %s! Your first message should be text-only and without any links or media. Just a heads up - if you don't follow this rule, you'll get banned from the group. Cheers!", cm.Language), nameString)
	msg := api.NewMessage(cm.ID, msgText)
	msg.ParseMode = api.ModeMarkdown
	msg.DisableNotification = true
	reply, err := b.Send(msg)
	if err != nil {
		return errors.WithMessage(err, "cant send")
	}
	go func() {
		time.Sleep(30 * time.Second)
		if err := bot.DeleteChatMessage(b, cm.ID, reply.MessageID); err != nil {
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

func (g *Gatekeeper) createCaptchaIndex(lang string) [][2]string {
	vars := g.Variants[lang]
	captchaIndex := make([][2]string, len(vars), len(vars))
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

func getLanguage(chat *api.Chat, user *api.User) string {
	if user != nil && tool.In(user.LanguageCode, []string{"en", "ru"}) {
		return user.LanguageCode
	}
	cm := db.MetaFromChat(chat, config.Get().DefaultLanguage)
	if cm != nil && tool.In(cm.Language, []string{"en", "ru"}) {
		return cm.Language
	}
	return config.Get().DefaultLanguage
}
