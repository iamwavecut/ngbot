package handlers

import (
	"compress/gzip"
	"context"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/bot"
	"github.com/iamwavecut/ngbot/db"
	"github.com/iamwavecut/ngbot/i18n"
	"github.com/iamwavecut/ngbot/infra"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	charadeShowWord    = "CHARADE_SHOW_WORD"
	charadeAnotherWord = "CHARADE_ANOTHER_WORD"
	charadeContinue    = "CHARADE_CONTINUE"
)

var charadeCallbackData = []string{charadeShowWord, charadeAnotherWord, charadeContinue}

type actType struct {
	userID     int
	winnerID   *int
	chatMeta   *db.ChatMeta
	word       string
	startTime  *time.Time
	finishTime *time.Time
}

type Charades struct {
	s      bot.Service
	path   string
	data   map[string]map[string]string
	active map[int64]*actType
}

func NewCharades(s bot.Service) *Charades {
	c := &Charades{
		s:      s,
		path:   infra.GetResourcesDir("charades"),
		data:   make(map[string]map[string]string),
		active: make(map[int64]*actType),
	}
	go c.dispatcher(context.Background()) // TODO graceful shutdown
	return c
}

func (c *Charades) dispatcher(ctx context.Context) {
	l := c.getLogEntry()
	l.Trace("dispatcher start")

	for {
		select {
		case <-ctx.Done():
			l.Trace("dispatcher end")
			return
		default:
		}
		//l.Trace("dispatcher work")
		time.Sleep(1 * time.Second)

		for chatID, act := range c.active {
			//l.Trace("dispatcher ", chatID)
			if act == nil {
				delete(c.active, chatID)
				continue
			}
			switch {
			case
				act.finishTime != nil &&
					time.Now().Unix()-(*act.finishTime).Unix() > 10:

				delete(c.active, chatID)

			case
				act.finishTime == nil &&
					act.startTime != nil &&
					time.Now().Unix()-(*act.startTime).Unix() > 3*60:

				delete(c.active, chatID)
				msg := tgbotapi.NewMessage(chatID, fmt.Sprintf(i18n.Get("Nobody guessed the word *%s*, that is sad.", act.chatMeta.Language), act.word))
				msg.ParseMode = "markdown"
				appendContinueKeyboard(&msg, act.chatMeta.Language)
				c.s.GetBot().Send(msg)
			}
		}
	}
}

func (c *Charades) Handle(u *tgbotapi.Update, cm *db.ChatMeta) (bool, error) {
	if cm == nil {
		return true, nil
	}

	switch cm.Type {
	case "supergroup", "group", "private": // TODO remove private
		break
	default:
		return true, nil
	}

	m := u.Message
	cb := u.CallbackQuery
	act := c.active[cm.ID]

	switch {
	case m != nil && m.From.IsBot:
		return true, nil
	case cb != nil && cb.From.IsBot:
		return true, nil
	}

	if cb != nil && isValidCharadeCallback(cb) {
		c.processCallback(cb, cm, act)
		return false, nil
	}

	if act != nil && m != nil && !m.IsCommand() {
		c.processAnswer(m, cm, act)
		return true, nil
	}

	if m != nil && m.IsCommand() && m.Command() == "charade" {
		c.processCommand(m, cm, act)
		return false, nil
	}

	return true, nil
}

func (c *Charades) processCommand(m *tgbotapi.Message, cm *db.ChatMeta, act *actType) {
	l := c.getLogEntry()
	l.Trace("charade trigger")
	if act != nil {
		userName, _ := bot.GetUN(m.From)
		c.s.GetBot().Send(tgbotapi.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("Charade is in progress, go and win it, %s!", cm.Language), userName)))
		return
	}

	err := c.startCharade(m.From, cm)
	if err != nil {
		l.WithError(err).Error("cant start charade")
	}
}

func (c *Charades) processAnswer(m *tgbotapi.Message, cm *db.ChatMeta, act *actType) {
	l := c.getLogEntry()
	if act.finishTime != nil {
		return
	}
	msg := tgbotapi.NewMessage(cm.ID, i18n.Get("Draw", cm.Language))
	msg.ParseMode = "markdown"
	appendContinueKeyboard(&msg, cm.Language)

	userWord := strings.ToLower(strings.Trim(m.Text, " \n\r?!.,;!@#$%^&*()/][\\"))
	userWord = strings.Replace(userWord, "ё", "е", -1)
	if userWord == strings.Replace(act.word, "ё", "е", -1) {
		if act.finishTime != nil {
			return
		}
		if m.From.ID != act.userID {
			userName, _ := bot.GetUN(m.From)
			msg.Text = fmt.Sprintf(i18n.Get("*%s* makes the right guess, smart pants!", cm.Language), userName)
			msg.ReplyToMessageID = m.MessageID
			act.finishTime = func() *time.Time { t := time.Now(); return &t }()
			act.winnerID = func() *int { ID := m.From.ID; return &ID }()

			winnerScore, err := c.s.GetDB().AddCharadeScore(cm.ID, m.From.ID)
			if err != nil {
				l.WithError(err).Error("cant add score for the winner")
			}
			explainerScore, err := c.s.GetDB().AddCharadeScore(cm.ID, act.userID)
			if err != nil {
				l.WithError(err).Error("cant add score for the explainer")
			}
			l.Info(winnerScore, explainerScore)
			//delete(c.active, cm.ID)

		} else {
			delete(c.active, cm.ID)
		}

		c.s.GetBot().Send(msg)
	}
}

func (c *Charades) processCallback(cb *tgbotapi.CallbackQuery, cm *db.ChatMeta, act *actType) {
	l := c.getLogEntry()
	l.Trace("charade valid")
	answer := tgbotapi.CallbackConfig{
		CallbackQueryID: cb.ID,
		Text:            i18n.Get("It's not your turn!", cm.Language),
	}

	switch cb.Data {

	case charadeShowWord:
		if act == nil || cb.From.ID != act.userID {
			break
		}

		if cb.From.ID == act.userID {
			answer.Text = act.word
			answer.ShowAlert = true
		}

	case charadeAnotherWord:
		if act == nil || cb.From.ID != act.userID {
			break
		}

		if cb.From.ID == act.userID {
			act, err := c.randomActForUser(cb.From.ID, cm)
			if err != nil {
				l.WithError(err).Error("cant get actType")
			}
			c.active[cm.ID] = act
			answer.Text = act.word
			answer.ShowAlert = true
		}

	case charadeContinue:
		switch {
		case
			act == nil,
			act.winnerID != nil && *act.winnerID == cb.From.ID,
			act.winnerID != nil && *act.winnerID != cb.From.ID && time.Now().Unix()-(*act.finishTime).Unix() >= 10:

			err := c.startCharade(cb.From, cm)
			if err != nil {
				l.WithError(err).Error("cant start charade continue")
			}
			answer.Text = ""

		case act != nil && act.winnerID != nil && *act.winnerID != cb.From.ID && time.Now().Unix()-(*act.finishTime).Unix() < 10:
			answer.Text = i18n.Get("The winner has 10 seconds advantage, try later", cm.Language)
		}
	}
	c.s.GetBot().Request(answer)
}

func (c *Charades) randomActForUser(userID int, cm *db.ChatMeta) (*actType, error) {
	if _, ok := c.data[cm.Language]; !ok {
		if err := c.load(cm.Language); err != nil {
			return nil, errors.WithMessagef(err, "cant load charades for %v", cm.Language)
		}
	}

	wordIndex := rand.Intn(len(c.data[cm.Language]))
	var currentIndex int
	var word string
	for currentWord := range c.data[cm.Language] {
		if wordIndex == currentIndex {
			word = currentWord
			break
		}
		currentIndex++
	}

	return &actType{
		userID:    userID,
		word:      word,
		startTime: func() *time.Time { t := time.Now(); return &t }(),
		chatMeta:  cm,
	}, nil
}

func (c *Charades) startCharade(user *tgbotapi.User, cm *db.ChatMeta) error {
	act, err := c.randomActForUser(user.ID, cm)
	if err != nil {
		return errors.WithMessage(err, "cant get actType")
	}

	c.active[cm.ID] = act
	userName, _ := bot.GetUN(user)
	msg := tgbotapi.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("Please, *%s*, explain the word, without using synonyms and other forms in three minutes. Both the explainer and the winner get a _point_ on success!", cm.Language), userName))
	msg.ParseMode = "markdown"
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(i18n.Get("💬 Show word", cm.Language), charadeShowWord),
			tgbotapi.NewInlineKeyboardButtonData(i18n.Get("🔄 Replace word", cm.Language), charadeAnotherWord),
		),
	)
	msg.ReplyMarkup = kb
	c.s.GetBot().Send(msg)

	return nil
}

func (c *Charades) load(lang string) error {
	l := c.getLogEntry()
	f, err := os.Open(filepath.Join(c.path, lang+".yml.gz"))
	if err != nil {
		return errors.WithMessagef(err, "cant open charades lang %s", lang)
	}

	r, err := gzip.NewReader(f)
	if err != nil {
		return errors.WithMessagef(err, "cant create reader lang %s", lang)
	}
	defer func() {
		if err := r.Close(); err != nil {
			l.WithError(err).Error("cant close reader")
		}
	}()

	res, err := ioutil.ReadAll(r)
	if err != nil {
		return errors.WithMessage(err, "cant read charades")
	}
	charades := make(map[string]string)
	if err = yaml.Unmarshal(res, &charades); err != nil {
		l.WithError(err).Errorln()
		return errors.WithMessage(err, "cant unmarshal charades")
	}

	c.data[lang] = charades
	return nil
}

func (c *Charades) getLogEntry() *log.Entry {
	return log.WithField("context", "charades")
}

func appendContinueKeyboard(msg *tgbotapi.MessageConfig, lang string) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(i18n.Get("I want to continue", lang), charadeContinue),
		),
	)
	msg.ReplyMarkup = kb
}

func isValidCharadeCallback(query *tgbotapi.CallbackQuery) bool {
	var res bool
	for _, data := range charadeCallbackData {
		if data == query.Data {
			res = true
		}
	}
	return res
}
