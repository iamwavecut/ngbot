package handlers

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
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

	defaultChallengeTimeout = 3 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute
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
	s          bot.Service
	joiners    map[int64]map[int64]*challengedUser
	newcomers  map[int64]map[int64]struct{}
	restricted map[int64]map[int64]struct{}

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
	entry := log.WithField("object", "Gatekeeper").WithField("method", "NewGatekeeper")
	entry.Debug("creating new gatekeeper")

	g := &Gatekeeper{
		s: s,

		joiners:    map[int64]map[int64]*challengedUser{},
		Variants:   map[string]map[string]string{},
		newcomers:  map[int64]map[int64]struct{}{},
		restricted: map[int64]map[int64]struct{}{},
	}

	for _, lang := range i18n.GetLanguagesList() {
		entry.Debugf("loading challenges for language: %s", lang)
		challengesData, err := resources.FS.ReadFile(infra.GetResourcesPath("gatekeeper", "challenges", lang+".yml"))
		if err != nil {
			entry.WithError(err).Errorf("cant load challenges file for language: %s", lang)
		}

		localVariants := map[string]string{}
		if err := yaml.Unmarshal(challengesData, &localVariants); err != nil {
			entry.WithError(err).Errorf("cant unmarshal challenges yaml for language: %s", lang)
		}
		g.Variants[lang] = localVariants
	}
	entry.Debug("Gatekeeper created successfully")
	return g
}

func (g *Gatekeeper) Handle(u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := g.getLogEntry().WithField("method", "Handle")
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
	if u.Message == nil && u.ChatJoinRequest == nil {
		entry.Debug("Update is not about join or message, not proceeding")
		return false, nil
	}
	entry.Debug("Update is about join or message, proceeding")

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
	settings, err := g.s.GetDB().GetSettings(chat.ID)
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
		err = g.s.GetDB().SetSettings(settings)
		if err != nil {
			entry.WithError(err).Error("failed to set chat settings")
		}
	}
	if !settings.Enabled {
		entry.Debug("gatekeeper is disabled for this chat")
		return true, nil
	}

	b := g.s.GetBot()
	m := u.Message

	entry.Debug("Checking if it's the first message")
	var isFirstMessage bool
	if m != nil && m.NewChatMembers != nil {
		isMember, err := g.s.GetDB().IsMember(chat.ID, user.ID)
		if err != nil {
			entry.WithError(err).Error("failed to check if user is a member")
		}
		isFirstMessage = !isMember
	}
	entry.Debugf("isFirstMessage: %v", isFirstMessage)

	entry.Debug("Determining update type")
	switch {
	case u.CallbackQuery != nil:
		entry.Debugf("callback query data: %s, user: %s", u.CallbackQuery.Data, bot.GetUN(user))
		return false, g.handleChallenge(u, chat, user)
	case u.ChatJoinRequest != nil:
		entry.Debug("handling chat join request")
		return true, g.handleChatJoinRequest(u)
	case m != nil && m.NewChatMembers != nil:
		entry.Debug("handling new chat members")
		chatInfo, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
			ChatConfig: api.ChatConfig{
				ChatID: m.Chat.ID,
			},
		})
		if err != nil {
			entry.WithError(err).Error("failed to get chat info")
			return true, err
		}
		if !chatInfo.JoinByRequest {
			entry.Debug("processing new chat members")
			return true, g.handleNewChatMembers(u, chat)
		}
		entry.Debug("ignoring invited and approved joins")
	case m != nil && m.From.ID == b.Self.ID && m.LeftChatMember != nil:
		entry.Debug("handling left chat member")
		return true, bot.DeleteChatMessage(b, chat.ID, m.MessageID)
	case isFirstMessage:
		entry.Debug("handling first message")
		return true, g.handleFirstMessage(u, chat, user)
	}
	entry.Debug("No specific handler matched, proceeding with default behavior")
	return true, nil
}

func (g *Gatekeeper) handleChallenge(u *api.Update, chat *api.Chat, user *api.User) (err error) {
	entry := g.getLogEntry().WithField("method", "handleChallenge")
	entry.Debug("handling challenge")
	b := g.s.GetBot()

	cq := u.CallbackQuery
	entry.Debugf("callback query data: %s, user: %s", cq.Data, bot.GetUN(user))

	joinerID, challengeUUID, err := func(s string) (int64, string, error) {
		entry := g.getLogEntry().WithField("method", "handleChallenge.parseCallbackData")
		entry.Debugf("parsing callback data: %s", s)
		parts := strings.Split(s, ";")
		if len(parts) != 2 {
			entry.Error("invalid string to split")
			return 0, "", errors.New("invalid string to split")
		}
		ID, err := strconv.ParseInt(parts[0], 10, 0)
		if err != nil {
			entry.WithError(err).Error("cant parse user ID")
			return 0, "", errors.WithMessage(err, "cant parse user ID")
		}
		entry.Debugf("parsed joinerID: %d, challengeUUID: %s", ID, parts[1])
		return ID, parts[1], nil
	}(cq.Data)
	if err != nil {
		entry.WithError(err).Error("failed to parse callback query data")
		return err
	}
	var isAdmin bool

	if chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
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
		entry.Debugf("user %s is admin: %v", bot.GetUN(user), isAdmin)
	} else {
		entry.WithError(err).Error("cant get chat member")
		return errors.New("cant get chat member")
	}

	lang := g.getLanguage(chat, user)
	entry.Debugf("using language: %s", lang)
	cu := g.extractChallengedUser(joinerID, chat.ID)
	if cu == nil {
		entry.Debug("no user matched for challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("This challenge isn't your concern", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}
		return nil
	}
	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: cu.targetChat.ID,
		},
	})
	if err != nil {
		entry.WithError(err).Error("cant get target chat info")
		return errors.WithMessage(err, "cant get target chat info")
	}
	lang = g.getLanguage(&targetChat, user)
	entry.Debugf("updated language: %s", lang)

	isPublic := cu.commChat.ID == cu.targetChat.ID
	entry.Debugf("isPublic: %v", isPublic)
	if !isPublic {
		isAdmin = false
	} else {
		entry.Debug("restricting chatting for user")
		_ = bot.RestrictChatting(b, user.ID, cu.targetChat.ID)
	}

	if !isAdmin && joinerID != user.ID {
		entry.Debug("user is not admin and not the joiner")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}
		return nil
	}

	switch {
	case isAdmin, cu.successUUID == challengeUUID:
		entry.Debugf("successful challenge for %s", bot.GetUN(cu.user))
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}

		if _, err = b.Request(api.NewDeleteMessage(cu.commChat.ID, cu.challengeMessageID)); err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		if !isPublic {
			entry.Debug("approving join request")
			_ = bot.ApproveJoinRequest(b, cu.user.ID, cu.targetChat.ID)
			msg := api.NewMessage(cu.commChat.ID, fmt.Sprintf(i18n.Get("Awesome, you're good to go! Feel free to start chatting in the group \"%s\".", lang), api.EscapeText(api.ModeMarkdown, cu.targetChat.Title)))
			msg.ParseMode = api.ModeMarkdown
			_ = tool.Err(b.Send(msg))
		} else {
			entry.Debug("unrestricting chatting for user")
			_ = bot.UnrestrictChatting(b, user.ID, cu.targetChat.ID)
		}

		if cu.successFunc != nil {
			entry.Debug("calling success function")
			cu.successFunc()
		}
		entry.Debug("Adding user to newcomers list")
		if _, ok := g.newcomers[cu.targetChat.ID]; !ok {
			entry.Debug("Initializing newcomers map for target chat")
			g.newcomers[cu.targetChat.ID] = map[int64]struct{}{}
		}
		if _, ok := g.newcomers[cu.targetChat.ID][cu.user.ID]; !ok {
			entry.Debugf("Adding user %d to newcomers list for chat %d", cu.user.ID, cu.targetChat.ID)
			g.newcomers[cu.targetChat.ID][cu.user.ID] = struct{}{}
		}
		entry.Debug("Adding user to restricted list")
		if _, ok := g.restricted[cu.targetChat.ID]; !ok {
			entry.Debug("Initializing restricted map for target chat")
			g.restricted[cu.targetChat.ID] = map[int64]struct{}{}
		}
		entry.Debugf("Removing challenged user %d from chat %d", cu.user.ID, cu.commChat.ID)
		g.removeChallengedUser(cu.user.ID, cu.commChat.ID)

	case cu.successUUID != challengeUUID:
		entry.Debugf("failed challenge for %s", bot.GetUN(cu.user))

		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, fmt.Sprintf(i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", lang), cu.targetChat.Title, 10))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}

		if !isPublic {
			entry.Debug("declining join request")
			_ = bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChat.ID)
			msg := api.NewMessage(cu.commChat.ID, fmt.Sprintf(i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", lang), cu.targetChat.Title, 10))
			msg.ParseMode = api.ModeMarkdown
			_ = tool.Err(b.Send(msg))
		}
		if cu.joinMessageID != 0 {
			entry.Debugf("Deleting join message %d from chat %d", cu.joinMessageID, cu.targetChat.ID)
			if err := bot.DeleteChatMessage(b, cu.targetChat.ID, cu.joinMessageID); err != nil {
				entry.WithError(err).Error("cant delete join message")
			}
		}

		entry.Debugf("Deleting challenge message %d from chat %d", cu.challengeMessageID, cu.commChat.ID)
		if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.challengeMessageID); err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		entry.Debugf("Banning user %d from chat %d", cu.user.ID, cu.targetChat.ID)
		if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChat.ID); err != nil {
			entry.WithError(err).Errorln("cant kick failed")
		}

		// stop timer anyway
		if cu.successFunc != nil {
			entry.Debug("Calling success function to stop timer")
			cu.successFunc()
		}
		entry.Debugf("Removing challenged user %d from chat %d", cu.user.ID, cu.commChat.ID)
		g.removeChallengedUser(cu.user.ID, cu.commChat.ID)
	default:
		entry.Debug("Unknown challenge result")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("I have no idea what is going on", lang))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
	}
	return err
}

func (g *Gatekeeper) handleNewChatMembers(u *api.Update, chat *api.Chat) error {
	entry := g.getLogEntry().WithField("method", "handleNewChatMembers")
	entry.Debug("Handling new chat members")
	return g.handleJoin(u, u.Message.NewChatMembers, chat, chat)
}

func (g *Gatekeeper) handleChatJoinRequest(u *api.Update) error {
	entry := g.getLogEntry().WithField("method", "handleChatJoinRequest")
	entry.Debug("Handling chat join request")
	target := &u.ChatJoinRequest.Chat

	entry.Debug("Getting bot chat info")
	comm, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	})
	if err != nil {
		entry.WithError(err).Error("Failed to get bot chat info")
		return err
	}

	return g.handleJoin(u, []api.User{u.ChatJoinRequest.From}, target, &comm)
}

func (g *Gatekeeper) handleJoin(u *api.Update, jus []api.User, target *api.Chat, comm *api.Chat) (err error) {
	entry := g.getLogEntry().WithField("method", "handleJoin")
	entry.Debug("Handling join")
	if target == nil || comm == nil {
		entry.Error("Target or comm is nil")
		return errors.New("target or comm is nil")
	}
	b := g.s.GetBot()

	for _, ju := range jus {
		if ju.IsBot {
			entry.Debugf("Skipping bot user %s", bot.GetUN(&ju))
			continue
		}

		entry.Debugf("Restricting user %s in chat %d", bot.GetUN(&ju), target.ID)
		if _, err := b.Request(api.RestrictChatMemberConfig{
			ChatMemberConfig: api.ChatMemberConfig{
				ChatConfig: api.ChatConfig{
					ChatID: target.ID,
				},
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
			entry.WithError(err).Error("Failed to restrict user")
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
		entry.Debugf("Adding challenged user %s to joiners list", bot.GetUN(cu.user))
		if _, ok := g.joiners[cu.commChat.ID]; !ok {
			g.joiners[cu.commChat.ID] = map[int64]*challengedUser{}
		}
		g.joiners[cu.commChat.ID][cu.user.ID] = cu
		commLang := g.getLanguage(cu.commChat, cu.user)

		go func() {
			entry.Debugf("Setting timer for %s", bot.GetUN(cu.user))
			timeout := time.NewTimer(3 * time.Minute)
			defer delete(g.joiners[cu.commChat.ID], cu.user.ID)

			select {
			case <-ctx.Done():
				entry.Infof("Removing challenge timer for %s", bot.GetUN(cu.user))
				timeout.Stop()
				return

			case <-timeout.C:
				entry.Infof("Challenge timed out for %s", bot.GetUN(cu.user))
				if cu.challengeMessageID != 0 {
					entry.Debugf("Deleting challenge message %d from chat %d", cu.challengeMessageID, cu.commChat.ID)
					if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.challengeMessageID); err != nil {
						entry.WithError(err).Error("Failed to delete challenge message")
					}
				}
				if cu.joinMessageID != 0 {
					entry.Debugf("Deleting join message %d from chat %d", cu.joinMessageID, cu.commChat.ID)
					if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.joinMessageID); err != nil {
						entry.WithError(err).Error("Failed to delete join message")
					}
				}
				entry.Debugf("Banning user %s from chat %d", bot.GetUN(cu.user), cu.targetChat.ID)
				if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChat.ID); err != nil {
					entry.WithError(err).Errorf("Failed to ban %s", bot.GetUN(cu.user))
				}
				if cu.commChat.ID != cu.targetChat.ID {
					entry.Debug("Declining join request")
					if err := bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChat.ID); err != nil {
						entry.WithError(err).Debug("Decline failed")
					}
					entry.Debug("Sending timeout message")
					sentMsg, err := b.Send(api.NewMessage(cu.commChat.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", commLang)))
					if err != nil {
						entry.WithError(err).Error("Failed to send timeout message")
					}
					time.AfterFunc(10*time.Minute, func() {
						entry.Debugf("Deleting timeout message %d from chat %d", sentMsg.MessageID, cu.commChat.ID)
						_ = bot.DeleteChatMessage(b, cu.commChat.ID, sentMsg.MessageID)
					})
				}
				return
			}
		}()

		entry.Debug("Creating captcha buttons")
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
		entry.Debug("Sending challenge message")
		sentMsg, err := b.Send(msg)
		if err != nil {
			entry.WithError(err).Error("Failed to send challenge message")
			return errors.WithMessage(err, "cant send")
		}
		cu.challengeMessageID = sentMsg.MessageID
	}
	entry.Debug("Exiting handleChallenge method")
	return nil
}

func (g *Gatekeeper) handleFirstMessage(u *api.Update, chat *api.Chat, user *api.User) error {
	entry := g.getLogEntry().WithField("method", "handleFirstMessage")
	entry.Debug("Entering handleFirstMessage method")

	b := g.s.GetBot()
	entry.Debug("Got bot instance")

	if u.FromChat() == nil {
		entry.Debug("FromChat is nil, returning")
		return nil
	}
	if u.SentFrom() == nil {
		entry.Debug("SentFrom is nil, returning")
		return nil
	}
	m := u.Message
	if m == nil {
		entry.Debug("Message is nil, returning")
		return nil
	}

	entry.Debug("Checking message text against static ban list")
	if m.Text != "" {
		for _, v := range []string{"Нужны сотрудников", "Без опыта роботы", "@dasha234di", "@marinetthr", "На момент стажировке", "5 р\\час"} {
			if strings.Contains(m.Text, v) {
				entry.Debugf("Found banned phrase: %s", v)
				if err := bot.DeleteChatMessage(b, m.Chat.ID, m.MessageID); err != nil {
					entry.WithError(err).Error("Can't delete message")
				}
				if err := bot.BanUserFromChat(b, m.From.ID, m.Chat.ID); err != nil {
					entry.WithField("user", bot.GetUN(user)).Info("Banned by static list")
					return errors.WithMessage(err, "Can't kick")
				}
				entry.Debug("User banned, returning")
				return nil
			}
		}
	}

	entry.Debug("Determining message type")
	toRestrict := true
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
	default:
		toRestrict = false
	}

	if !toRestrict {
		entry.Debug("Message doesn't need restriction, removing user from restricted and newcomers lists")
		delete(g.restricted[chat.ID], user.ID)
		delete(g.newcomers[chat.ID], user.ID)
		return nil
	}
	entry.Debug("Restricting user")
	if err := bot.DeleteChatMessage(b, chat.ID, m.MessageID); err != nil {
		entry.WithError(err).Error("Can't delete first message")
	}
	entry.Debug("Applying chat restrictions")
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
		entry.WithError(err).Error("Can't restrict user")
		return errors.WithMessage(err, "Can't restrict")
	}

	entry.Debug("Checking for second violation")
	_, secondViolation := g.restricted[chat.ID][user.ID]
	if secondViolation {
		entry.Debug("Second violation detected, kicking user")
		err := bot.BanUserFromChat(b, user.ID, chat.ID)
		if err != nil {
			entry.WithError(err).Error("Can't kick user")
			return errors.WithMessage(err, "Can't kick")
		}
		entry.Debug("Removing user from restricted and newcomers lists")
		delete(g.restricted[chat.ID], user.ID)
		delete(g.newcomers[chat.ID], user.ID)
		return nil
	}
	entry.Debug("First violation")

	entry.Debug("Adding user to restricted list")
	g.restricted[chat.ID][user.ID] = struct{}{}
	lang := g.getLanguage(chat, user)
	entry.Debugf("Using language: %s", lang)
	nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, bot.GetFullName(user)), user.ID)
	msgText := fmt.Sprintf(i18n.Get("Hi %s! Your first message should be text-only and without any links or media. Just a heads up - if you don't follow this rule, you'll get banned from the group. Cheers!", lang), nameString)
	msg := api.NewMessage(chat.ID, msgText)
	msg.ParseMode = api.ModeMarkdown
	msg.DisableNotification = true
	entry.Debug("Sending warning message")
	reply, err := b.Send(msg)
	if err != nil {
		entry.WithError(err).Error("Can't send warning message")
		return errors.WithMessage(err, "Can't send")
	}
	entry.Debug("Starting goroutine to delete warning message after 30 seconds")
	go func() {
		time.Sleep(30 * time.Second)
		if err := bot.DeleteChatMessage(b, chat.ID, reply.MessageID); err != nil {
			entry.WithError(err).Error("Can't delete warning message")
		}
	}()

	entry.Debug("Exiting handleFirstMessage method")
	return nil
}

func (g *Gatekeeper) extractChallengedUser(userID int64, chatID int64) *challengedUser {
	entry := g.getLogEntry().WithField("method", "extractChallengedUser")
	entry.Debug("Entering extractChallengedUser method")

	joiner := g.findChallengedUser(userID, chatID)
	if joiner == nil {
		entry.Debug("No challenged user found")
		return nil
	}
	entry.Debug("Removing challenged user from joiners list")
	delete(g.joiners[chatID], userID)

	entry.Debug("Exiting extractChallengedUser method")
	return joiner
}

func (g *Gatekeeper) findChallengedUser(userID int64, chatID int64) *challengedUser {
	entry := g.getLogEntry().WithField("method", "findChallengedUser")
	entry.Debug("Entering findChallengedUser method")

	if _, ok := g.joiners[chatID]; !ok {
		entry.Warn("No challenges for chat", chatID)
		return nil
	}
	if user, ok := g.joiners[chatID][userID]; ok {
		entry.Debug("Challenged user found")
		return user
	}

	entry.Warn("No challenges for chat user", chatID, userID)
	entry.Debug("Exiting findChallengedUser method")
	return nil
}

func (g *Gatekeeper) removeChallengedUser(userID int64, chatID int64) {
	entry := g.getLogEntry().WithField("method", "removeChallengedUser")
	entry.Debug("Entering removeChallengedUser method")

	if _, ok := g.joiners[chatID]; !ok {
		entry.Trace("No challenges for chat", chatID)
		return
	}
	if _, ok := g.joiners[chatID][userID]; ok {
		entry.Debug("Removing challenged user")
		delete(g.joiners[chatID], userID)
		return
	}
	entry.Trace("No challenges for chat user", chatID, userID)
	entry.Debug("Exiting removeChallengedUser method")
}

func (g *Gatekeeper) createCaptchaIndex(lang string) [][2]string {
	entry := g.getLogEntry().WithField("method", "createCaptchaIndex")
	entry.Debug("Entering createCaptchaIndex method")

	vars := g.Variants[lang]
	captchaIndex := make([][2]string, len(vars))
	idx := 0
	for k, v := range vars {
		captchaIndex[idx] = [2]string{k, v}
		idx++
	}

	entry.Debug("Exiting createCaptchaIndex method")
	return captchaIndex
}

func (g *Gatekeeper) createCaptchaButtons(cu *challengedUser, lang string) ([]api.InlineKeyboardButton, [2]string) {
	entry := g.getLogEntry().WithField("method", "createCaptchaButtons")
	entry.Debug("Entering createCaptchaButtons method")

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

	entry.Debug("Exiting createCaptchaButtons method")
	return buttons, correctVariant
}

func (g *Gatekeeper) getLogEntry() *log.Entry {
	return log.WithField("context", "gatekeeper")
}

func (g *Gatekeeper) getLanguage(chat *api.Chat, user *api.User) string {
	entry := g.getLogEntry().WithField("method", "getLanguage")
	entry.Debug("Entering getLanguage method")

	if settings, err := g.s.GetDB().GetSettings(chat.ID); !tool.Try(err) {
		entry.Debug("Using language from chat settings")
		return settings.Language
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		entry.Debug("Using language from user settings")
		return user.LanguageCode
	}
	entry.Debug("Using default language")

	entry.Debug("Exiting getLanguage method")
	return config.Get().DefaultLanguage
}
