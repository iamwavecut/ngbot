package handlers

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/config"
	"github.com/iamwavecut/ngbot/ngbot"
	"github.com/iamwavecut/ngbot/ngbot/i18n"
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
	joiners map[*tgbotapi.Chat][]*challengedUser

	Challenges struct {
		Variants map[string]string `yaml:"variants"`
	}
}

func NewGatekeeper(cfg *config.Config, bot *tgbotapi.BotAPI) *Gatekeeper {
	g := &Gatekeeper{
		cfg:     cfg,
		bot:     bot,
		joiners: make(map[*tgbotapi.Chat][]*challengedUser),
	}

	challengesData, err := ioutil.ReadFile(fmt.Sprintf("resources/challenges.%s.yml", cfg.Language))
	if err != nil {
		log.WithError(err).Errorln("cant load challenges file")
	}

	if err := yaml.Unmarshal(challengesData, &g.Challenges); err != nil {
		log.WithError(err).Errorln("cant unmarshal challenges yaml")
	}

	return g
}

func (g *Gatekeeper) Handle(u tgbotapi.Update) (proceed bool, err error) {
	switch {
	case u.CallbackQuery != nil:
		err = g.handleChallenge(u)

	case u.Message.NewChatMembers != nil:
		err = g.handleNewChatMembers(u)
	}

	return true, err
}

func (g *Gatekeeper) handleChallenge(u tgbotapi.Update) (err error) {
	cq := u.CallbackQuery
	log.Traceln(spew.Sdump(cq))
	users, ok := g.joiners[cq.Message.Chat]
	if !ok {
		log.Warnln("no challenges for chat", cq.Message.Chat.ID)
		return nil
	}

	var joiner *challengedUser
	for _, user := range users {
		if user.user.ID == cq.From.ID {
			joiner = user
			break
		}
	}
	if joiner == nil {
		log.Debug("no user matched for challenge in chat", cq.Message.Chat.ID)
		return nil
	}

	switch cq.Data {
	case challengeSucceeded:
		log.Debug("successful challenge")

		if joiner.successFunc != nil {
			joiner.successFunc()
		}

	case challengeFailed:
		log.Debug("failed challenge")

		// stop timer anyway
		if joiner.successFunc != nil {
			joiner.successFunc()
		}

		err = g.kickUserFromChat(joiner.user, cq.Message.Chat)
		if err != nil {
			log.WithError(err).Errorln("cant kick failed")
		}
	}

	if _, err := g.bot.Send(tgbotapi.NewCallback(cq.ID, i18n.Get("Thanks for answer"))); err != nil {
		log.WithError(err).Errorln("cant answer callback query")
	}

	return err
}

func (g *Gatekeeper) handleNewChatMembers(u tgbotapi.Update) error {
	n := u.Message.NewChatMembers

	var challengedUsers = make([]*challengedUser, len(n), len(n))
	var wg sync.WaitGroup
	wg.Add(len(n))

	for i, joinedUser := range n {
		if joinedUser.IsBot {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		name, _ := ngbot.GetFullName(&joinedUser)
		challengedUsers[i] = &challengedUser{
			user:        &joinedUser,
			successFunc: cancel,
			name:        name,
		}
		go func() {
			defer wg.Done()
			timeout := time.NewTimer(5 * time.Minute)

			select {
			case <-ctx.Done():
				log.Info("aborting challenge timer")
				timeout.Stop()
			case <-timeout.C:
				log.Info("challenge timed out")
				cancel()
				if err := g.kickUserFromChat(&joinedUser, u.Message.Chat); err != nil {
					return
				}
			}
		}()
	}

	if len(challengedUsers) == 0 {
		delete(g.joiners, u.Message.Chat)
		return nil
	}

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

	wg.Wait()
	delete(g.joiners, u.Message.Chat)
	_, err = g.bot.Send(tgbotapi.NewDeleteMessage(sentMsg.Chat.ID, sentMsg.MessageID))
	if err != nil {
		return errors.Wrap(err, "cant delete")
	}

	return nil
}

func (g *Gatekeeper) kickUserFromChat(user *tgbotapi.User, chat *tgbotapi.Chat) error {
	log.Traceln("kicking user")
	_, err := g.bot.Send(tgbotapi.KickChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{
			ChatID: chat.ID,
			UserID: user.ID,
		},
		UntilDate: time.Now().Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return errors.Wrap(err, "cant kick")
	}

	return nil
}
