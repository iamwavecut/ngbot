package handlers

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
}

type Gatekeeper struct {
	s                bot.Service
	joiners          map[int64]map[int64]*challengedUser
	welcomeMessageID int

	Variants map[string]map[string]string `yaml:"variants"`
}

func NewGatekeeper(s bot.Service) *Gatekeeper {
	g := &Gatekeeper{
		s: s,

		joiners:  map[int64]map[int64]*challengedUser{},
		Variants: map[string]map[string]string{},
	}

	entry := g.getLogEntry()

	for _, lang := range [2]string{"en", "ru"} {
		entry.Traceln("loading localized challenges")
		challengesData, err := resources.FS.ReadFile(filepath.Join(infra.GetResourcesDir("gatekeeper", "challenges"), lang+".yml"))
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

	switch {
	case u.CallbackQuery != nil:
		entry.Traceln("handle challenge")
		return false, g.handleChallenge(u, cm, um)
	case m != nil && m.NewChatMembers != nil:
		entry.Traceln("handle new chat members")
		return true, g.handleNewChatMembers(u, cm, um)
	case m != nil && m.From.ID == b.Self.ID && m.LeftChatMember != nil:
		return true, bot.DeleteChatMessage(b, cm.ID, m.MessageID)
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

	chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			UserID: um.ID,
			ChatID: cm.ID,
		},
	})
	if err != nil {
		return errors.New("cant get chat member")
	}
	var isAdmin bool
	switch {
	case
		chatMember.IsCreator(),
		chatMember.IsAdministrator() && chatMember.CanRestrictMembers:
		isAdmin = true
	}

	if !isAdmin && joinerID != um.ID {
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}
	joiner := g.extractChallengedUser(joinerID, cm.ID)
	if joiner == nil {
		entry.Debug("no user matched for challenge in chat ", cm.Title)
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("No challenge for you", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}

	switch {
	case isAdmin, joiner.successUUID == challengeUUID:
		entry.Debug("successful challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Welcome, bro!", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		_, err = b.Request(api.NewDeleteMessage(cm.ID, joiner.challengeMessageID))
		if err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		if joiner.successFunc != nil {
			joiner.successFunc()
		}

	case joiner.successUUID != challengeUUID:
		entry.Debug("failed challenge")
		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		if err := bot.DeleteChatMessage(b, cm.ID, joiner.joinMessageID); err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		if err := bot.DeleteChatMessage(b, cm.ID, joiner.challengeMessageID); err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		if err := bot.KickUserFromChat(b, joiner.user.ID, cm.ID); err != nil {
			entry.WithError(err).Errorln("cant kick failed")
		}

		// stop timer anyway
		if joiner.successFunc != nil {
			joiner.successFunc()
		}

	default:
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("I have no idea what is going on", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
	}
	return err
}

func (g *Gatekeeper) handleNewChatMembers(u *api.Update, cm *db.ChatMeta, _ *db.UserMeta) error {
	entry := g.getLogEntry()
	b := g.s.GetBot()

	n := u.Message.NewChatMembers
	entry.Traceln("handle new", len(n))

	captchaIndex := make([][2]string, len(g.Variants[cm.Language]), len(g.Variants[cm.Language]))
	idx := 0
	for k, v := range g.Variants[cm.Language] {
		captchaIndex[idx] = [2]string{k, v}
		idx++
	}

	for _, joinedUser := range n {
		jum := db.MetaFromUser(&joinedUser)
		if jum.IsBot {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)

		cu := &challengedUser{
			user:          jum,
			successFunc:   cancel,
			name:          bot.EscapeMarkdown(jum.GetFullName()),
			joinMessageID: u.Message.MessageID,
			successUUID:   uuid.New(),
		}
		if _, ok := g.joiners[cm.ID]; !ok {
			g.joiners[cm.ID] = map[int64]*challengedUser{}
		}
		g.joiners[cm.ID][jum.ID] = cu

		if err := bot.RestrictChatting(b, jum.ID, cm.ID); err != nil {
			entry.Traceln("restrict failed", err)
		}
		go func() {
			entry.Traceln("setting timer for", jum.GetUN())
			timeout := time.NewTimer(3 * time.Minute)

			select {
			case <-ctx.Done():
				entry.Info("aborting challenge timer")
				timeout.Stop()
				delete(g.joiners[cm.ID], cu.user.ID)
				if err := bot.UnrestrictChatting(b, jum.ID, cm.ID); err != nil {
					entry.Traceln("unrestrict failed", err)
				}
			case <-timeout.C:
				entry.Info("challenge timed out")
				if err := bot.DeleteChatMessage(b, cm.ID, cu.joinMessageID); err != nil {
					entry.WithError(err).Error("cant delete join message")
				}
				if err := bot.DeleteChatMessage(b, cm.ID, cu.challengeMessageID); err != nil {
					entry.WithError(err).Error("cant delete challenge message")
				}
				if err := bot.UnrestrictChatting(b, jum.ID, cm.ID); err != nil {
					entry.Traceln("unrestrict failed", err)
				}
				if err := bot.KickUserFromChat(b, jum.ID, cm.ID); err != nil {
					return
				}
			}
		}()

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

		nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", cu.user.GetFullName(), cu.user.ID)
		msgText := fmt.Sprintf(i18n.Get("Hi there, %s! Please, pick %s to prove that you're human being (or be banned otherwise)", cm.Language), nameString, correctVariant[1])
		msg := api.NewMessage(cm.ID, msgText)
		msg.ParseMode = "markdown"
		msg.DisableNotification = true

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

func (g *Gatekeeper) getLogEntry() *log.Entry {
	return log.WithField("context", "gatekeeper")
}
