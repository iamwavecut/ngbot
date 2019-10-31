package handlers

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/bot"
	"github.com/iamwavecut/ngbot/config"
	"github.com/iamwavecut/ngbot/i18n"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	challengeSucceeded = "CHALLENGE_ACCEPTED"
	challengeFailed    = "CHALLENGE_FAILED"
)

type challengedUser struct {
	user        *tgbotapi.User
	successFunc func()
	name        string
}

type Gatekeeper struct {
	cfg     *config.Config
	bot     *tgbotapi.BotAPI
	joiners map[int64][]*challengedUser

	Challenges struct {
		Variants map[string]string `yaml:"variants"`
	}
}

func NewGatekeeper(cfg *config.Config, bot *tgbotapi.BotAPI) *Gatekeeper {
	g := &Gatekeeper{
		cfg:     cfg,
		bot:     bot,
		joiners: make(map[int64][]*challengedUser),
	}
	entry := g.getLogEntry()

	entry.Traceln("loading localized challenges")
	challengesData, err := ioutil.ReadFile(fmt.Sprintf("resources/challenges.%s.yml", cfg.Language))
	if err != nil {
		entry.WithError(err).Errorln("cant load challenges file")
	}

	entry.Traceln("unmarshal localized challenges")
	if err := yaml.Unmarshal(challengesData, &g.Challenges); err != nil {
		entry.WithError(err).Errorln("cant unmarshal challenges yaml")
	}

	return g
}

func (g *Gatekeeper) Handle(u tgbotapi.Update) (proceed bool, err error) {
	entry := g.getLogEntry()

	switch {
	case u.CallbackQuery != nil:
		entry.Traceln("handle challenge")
		err = g.handleChallenge(u)

	case u.Message != nil && u.Message.NewChatMembers != nil:
		entry.Traceln("handle new chat members")
		err = g.handleNewChatMembers(u)

	case u.Message != nil && u.Message.NewChatMembers == nil:
		//entry.Traceln("handle challenge message")
		//err = g.deleteChallengedMessages(u)
	}

	return true, err
}

func (g *Gatekeeper) handleChallenge(u tgbotapi.Update) (err error) {
	entry := g.getLogEntry()

	cq := u.CallbackQuery
	entry.Traceln(cq.Data, cq.From.UserName)

	joiner := g.extractChallengedUser(cq.From.ID, cq.Message.Chat.ID)
	if joiner == nil {
		entry.Debug("no user matched for challenge in chat", cq.Message.Chat.ID)
		if _, err := g.bot.Request(tgbotapi.NewCallback(cq.ID, i18n.Get("Stop it! You're too real"))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		return nil
	}

	switch cq.Data {
	case challengeSucceeded:
		entry.Debug("successful challenge")
		if _, err := g.bot.Request(tgbotapi.NewCallback(cq.ID, i18n.Get("Welcome, bro!"))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		if joiner.successFunc != nil {
			joiner.successFunc()
		}

	case challengeFailed:
		entry.Debug("failed challenge")
		if _, err := g.bot.Request(tgbotapi.NewCallbackWithAlert(cq.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes"))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
		// stop timer anyway
		if joiner.successFunc != nil {
			joiner.successFunc()
		}

		err = bot.KickUserFromChat(g.bot, joiner.user.ID, cq.Message.Chat.ID)
		if err != nil {
			entry.WithError(err).Errorln("cant kick failed")
		}

	default:
		if _, err := g.bot.Request(tgbotapi.NewCallback(cq.ID, i18n.Get("I have no idea what is going on"))); err != nil {
			entry.WithError(err).Errorln("cant answer callback query")
		}
	}

	return err
}

func (g *Gatekeeper) deleteChallengedMessages(u tgbotapi.Update) error {
	entry := g.getLogEntry()
	m := u.Message

	if _, ok := g.joiners[m.Chat.ID]; !ok {
		return nil
	}

	joiner, _ := g.findChallengedUser(m.From.ID, m.Chat.ID)
	if joiner == nil {
		entry.Traceln("user is not challenged")
		return nil
	}

	_, err := g.bot.Request(tgbotapi.NewDeleteMessage(m.Chat.ID, m.MessageID))
	if err != nil {
		entry.WithError(err).Error("cant delete challenged user message")
	}

	return nil
}

func (g *Gatekeeper) handleNewChatMembers(u tgbotapi.Update) error {
	entry := g.getLogEntry()

	n := u.Message.NewChatMembers
	entry.Traceln("handle new", len(n))

	var challengedUsers = make([]*challengedUser, len(n), len(n))
	var wg sync.WaitGroup
	wg.Add(len(n))

	for i, joinedUser := range n {
		if joinedUser.IsBot {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		name, _ := bot.GetFullName(&joinedUser)
		challengedUsers[i] = &challengedUser{
			user:        &joinedUser,
			successFunc: cancel,
			name:        name,
		}
		go func() {
			defer wg.Done()
			entry.Traceln("setting timer for", joinedUser.UserName)
			timeout := time.NewTimer(3 * time.Minute)

			select {
			case <-ctx.Done():
				entry.Info("aborting challenge timer")
				timeout.Stop()
			case <-timeout.C:
				entry.Info("challenge timed out")
				cancel()
				if err := bot.KickUserFromChat(g.bot, joinedUser.ID, u.Message.Chat.ID); err != nil {
					return
				}
			}
		}()
	}

	if len(challengedUsers) == 0 {
		return nil
	}
	if len(g.joiners[u.Message.Chat.ID]) == 0 {
		g.joiners[u.Message.Chat.ID] = challengedUsers
	}
	g.joiners[u.Message.Chat.ID] = append(g.joiners[u.Message.Chat.ID], challengedUsers...)

	captchaIndex := make([][2]string, len(g.Challenges.Variants), len(g.Challenges.Variants))
	idx := 0
	for k, v := range g.Challenges.Variants {
		captchaIndex[idx] = [2]string{k, v}
		idx++
	}

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

	var namesList []string
	for _, joinedUser := range challengedUsers {
		namesList = append(namesList, fmt.Sprintf("[%s](tg://user?id=%d)", joinedUser.name, joinedUser.user.ID))
	}

	msgText := fmt.Sprintf(i18n.Get("Hi there, %s! Please, pick %s to bypass bot test (or be banned)"), strings.Join(namesList, ", "), correctVariant[1])
	msg := tgbotapi.NewMessage(u.Message.Chat.ID, msgText)
	msg.ParseMode = "markdown"

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(buttons...),
	)
	msg.ReplyMarkup = kb
	sentMsg, err := g.bot.Send(msg)
	if err != nil {
		return errors.Wrap(err, "cant send")
	}

	entry.Traceln("waiting for challenge")
	go func() {
		wg.Wait()
		_, err = g.bot.Request(tgbotapi.NewDeleteMessage(sentMsg.Chat.ID, sentMsg.MessageID))
		if err != nil {
			entry.WithError(err).Error("cant delete")
		}
		_, err = g.bot.Request(tgbotapi.NewDeleteMessage(u.Message.Chat.ID, u.Message.MessageID))
		if err != nil {
			entry.WithError(err).Error("cant delete")
		}
		entry.Traceln("end challenge")
	}()

	return nil
}

func (g *Gatekeeper) extractChallengedUser(userID int, chatID int64) *challengedUser {
	users, ok := g.joiners[chatID]
	if !ok {
		g.getLogEntry().Warnln("no challenges for chat", chatID)
		return nil
	}

	joiner, i := g.findChallengedUser(userID, chatID)
	if joiner == nil {
		return nil
	}
	if i < len(users)-1 {
		copy(users[i:], users[i+1:])
	}
	users[len(users)-1] = nil
	users = users[:len(users)-1]
	g.joiners[chatID] = users

	return joiner
}

func (g *Gatekeeper) findChallengedUser(userID int, chatID int64) (*challengedUser, int) {
	users, ok := g.joiners[chatID]
	if !ok {
		g.getLogEntry().Warnln("no challenges for chat", chatID)
		return nil, -1
	}

	for i, user := range users {
		if user.user.ID == userID {
			return user, i
		}
	}

	return nil, -1
}

func (g *Gatekeeper) getLogEntry() *log.Entry {
	return log.WithField("context", "gatekeeper")
}
