package handlers

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/config"
	"github.com/iamwavecut/ngbot/ngbot"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type challengedUser struct {
	user tgbotapi.User
	ctx  context.Context
	name string
}

type Gatekeeper struct {
	cfg     *config.Config
	bot     *tgbotapi.BotAPI
	joiners map[*tgbotapi.Chat][]*challengedUser
}

func NewGatekeeper(cfg *config.Config, bot *tgbotapi.BotAPI) *Gatekeeper {
	return &Gatekeeper{
		cfg:     cfg,
		bot:     bot,
		joiners: make(map[*tgbotapi.Chat][]*challengedUser),
	}
}

func (g *Gatekeeper) Handle(u tgbotapi.Update) (proceed bool, err error) {
	m := u.Message

	switch {
	case m.NewChatMembers != nil:
		err = g.handleNewChatMembers(u)
	}

	return true, err
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
			user: joinedUser,
			ctx:  ctx,
			name: name,
		}
		go func() {
			defer wg.Done()
			timeout := time.NewTimer(1 * time.Minute)

			select {
			case <-ctx.Done():
				log.Info("user challenge success, aborting timer")
				timeout.Stop()
			case <-timeout.C:
				log.Info("user challenge failure, timed out")
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

	var captchaVariants = map[string]string{
		"🐩":  "пуделя",
		"🐿️": "белку",
		"🐓":  "петуха",
		"🐷":  "харам",
		"🍆":  "елдак",
		"🎂":  "торт",
		"🍔":  "бургер",
		"🔪":  "нож",
		"📱":  "айфон",
		"🎁":  "сектор приз на барабане",
		"🖥️": "комплюктер",
		"💡":  "стул",
		"🥁":  "барабан",
		"🎸":  "гитару",
		"❤️": "лайк",
		"💩":  "💩 (твой код)",
		"🧦":  "носки",
		"🌭":  "хот-дог",
		"🍌":  "банан",
		"🍎":  "яблоко",
		"🐐":  "козла",
		"💺💺": "два стула",
	}

	captchaIndex := make([][2]string, len(captchaVariants), len(captchaVariants))
	idx := 0
	for k, v := range captchaVariants {
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
		isCorrectStr := "0"
		if v[0] == correctVariant[0] {
			isCorrectStr = "1"
		}
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(v[0], isCorrectStr))
	}

	var namesList []string
	for _, joinedUser := range challengedUsers {
		namesList = append(namesList, joinedUser.name)
	}

	msgText := fmt.Sprintf("Привет, %s! В качестве приветствия нажми на %s :)", strings.Join(namesList, ", "), correctVariant[1])
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
