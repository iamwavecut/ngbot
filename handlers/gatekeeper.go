package handlers

import (
	"context"
	"fmt"
	"github.com/iamwavecut/ngbot/db"
	"github.com/iamwavecut/ngbot/infra"
	"io/ioutil"
	"math/rand"
	"path/filepath"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/bot"
	"github.com/iamwavecut/ngbot/i18n"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	challengeSucceeded = "CHALLENGE_ACCEPTED"
	challengeFailed    = "CHALLENGE_FAILED"
)

var challengeCallbackData = []string{challengeSucceeded, challengeFailed}

type challengedUser struct {
	user               *tgbotapi.User
	successFunc        func()
	name               string
	joinMessageID      int
	challengeMessageID int
}

type Gatekeeper struct {
	s       bot.Service
	joiners map[int64]map[int]*challengedUser

	Variants map[string]map[string]string `yaml:"variants"`
}

func NewGatekeeper(s bot.Service) *Gatekeeper {
	g := &Gatekeeper{
		s:        s,
		joiners:  map[int64]map[int]*challengedUser{},
		Variants: map[string]map[string]string{},
	}

	entry := g.getLogEntry()

	for _, lang := range [2]string{"en", "ru"} {
		entry.Traceln("loading localized challenges")
		challengesData, err := ioutil.ReadFile(filepath.Join(infra.GetResourcesDir("gatekeeper", "challenges"), lang+".yml"))
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

func (g *Gatekeeper) Handle(u *tgbotapi.Update, cm *db.ChatMeta) (bool, error) {
	if cm == nil {
		return true, nil
	}
	entry := g.getLogEntry()

	switch {
	case u.CallbackQuery != nil && isValidChallengeCallback(u.CallbackQuery):
		entry.Traceln("handle challenge")
		return false, g.handleChallenge(u, cm)

	case u.Message != nil && u.Message.NewChatMembers != nil:
		entry.Traceln("handle new chat members")

		return true, g.handleNewChatMembers(u, cm)
	}
	return true, nil
}

func isValidChallengeCallback(query *tgbotapi.CallbackQuery) bool {
	var res bool
	for _, data := range challengeCallbackData {
		if data == query.Data {
			res = true
		}
	}
	return res
}

func (g *Gatekeeper) handleChallenge(u *tgbotapi.Update, cm *db.ChatMeta) (err error) {
	entry := g.getLogEntry()
	b := g.s.GetBot()

	cq := u.CallbackQuery
	entry.Traceln(cq.Data, cq.From.UserName)

	joiner := g.extractChallengedUser(cq.From.ID, cm.ID)
	if joiner == nil {
		entry.Debug("no user matched for challenge in chat ", cm.Title)
		if _, err := b.Request(tgbotapi.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}

	switch cq.Data {
	case challengeSucceeded:
		entry.Debug("successful challenge")
		if _, err := b.Request(tgbotapi.NewCallback(cq.ID, i18n.Get("Welcome, bro!", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		_, err = b.Request(tgbotapi.NewDeleteMessage(cm.ID, joiner.challengeMessageID))
		if err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		if joiner.successFunc != nil {
			joiner.successFunc()
		}

	case challengeFailed:
		entry.Debug("failed challenge")
		if _, err := b.Request(tgbotapi.NewCallbackWithAlert(cq.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}

		_, err := b.Request(tgbotapi.NewDeleteMessage(cm.ID, joiner.joinMessageID))
		if err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		_, err = b.Request(tgbotapi.NewDeleteMessage(cm.ID, joiner.challengeMessageID))
		if err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		err = bot.KickUserFromChat(b, joiner.user.ID, cm.ID)
		if err != nil {
			entry.WithError(err).Errorln("cant kick failed")
		}

		// stop timer anyway
		if joiner.successFunc != nil {
			joiner.successFunc()
		}

	default:
		if _, err := b.Request(tgbotapi.NewCallback(cq.ID, i18n.Get("I have no idea what is going on", cm.Language))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
	}
	return err
}

func (g *Gatekeeper) handleNewChatMembers(u *tgbotapi.Update, cm *db.ChatMeta) error {
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
		if joinedUser.IsBot {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		name, _ := bot.GetFullName(&joinedUser)
		cu := &challengedUser{
			user:          &joinedUser,
			successFunc:   cancel,
			name:          name,
			joinMessageID: u.Message.MessageID,
		}
		if _, ok := g.joiners[cm.ID]; !ok {
			g.joiners[cm.ID] = map[int]*challengedUser{}
		}
		g.joiners[cm.ID][joinedUser.ID] = cu
		go func() {
			entry.Traceln("setting timer for", joinedUser.UserName)
			timeout := time.NewTimer(3 * time.Minute)

			select {
			case <-ctx.Done():
				entry.Info("aborting challenge timer")
				timeout.Stop()
				delete(g.joiners[cm.ID], cu.user.ID)
			case <-timeout.C:
				entry.Info("challenge timed out")
				_, err := b.Request(tgbotapi.NewDeleteMessage(cm.ID, cu.joinMessageID))
				if err != nil {
					entry.WithError(err).Error("cant delete join message")
				}
				_, err = b.Request(tgbotapi.NewDeleteMessage(cm.ID, cu.challengeMessageID))
				if err != nil {
					entry.WithError(err).Error("cant delete challenge message")
				}
				if err := bot.KickUserFromChat(b, joinedUser.ID, cm.ID); err != nil {
					return
				}
			}
		}()

		captchaRandomSet := make([][2]string, 0, 3)
		usedIDs := make(map[int]struct{}, 3)
		for len(captchaRandomSet) < 3 {
			ID := rand.Intn(len(captchaIndex))
			if _, ok := usedIDs[ID]; ok {
				continue
			}
			captchaRandomSet = append(captchaRandomSet, captchaIndex[ID])
			usedIDs[ID] = struct{}{}
		}
		correctVariant := captchaRandomSet[rand.Intn(3)]
		var buttons []tgbotapi.InlineKeyboardButton
		for _, v := range captchaRandomSet {
			result := challengeFailed
			if v[0] == correctVariant[0] {
				result = challengeSucceeded
			}
			buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(v[0], result))
		}

		msgText := fmt.Sprintf(i18n.Get("Hi there, %s! Please, pick %s to bypass bot test (or be banned)", cm.Language), cu.name, correctVariant[1])
		msg := tgbotapi.NewMessage(cm.ID, msgText)
		msg.ParseMode = "markdown"

		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(buttons...),
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

func (g *Gatekeeper) extractChallengedUser(userID int, chatID int64) *challengedUser {
	joiner := g.findChallengedUser(userID, chatID)
	if joiner == nil {
		return nil
	}
	delete(g.joiners[chatID], userID)
	return joiner
}

func (g *Gatekeeper) findChallengedUser(userID int, chatID int64) *challengedUser {
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
