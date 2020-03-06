package handlers

import (
	"compress/gzip"
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

const charadeShowWord = "CHARADE_SHOW_WORD"
const charadeAnotherWord = "CHARADE_ANOTHER_WORD"
const charadeContinue = "CHARADE_CONTINUE"

var charadeCallbackData = []string{charadeShowWord, charadeAnotherWord, charadeContinue}

type act struct {
	userID     int
	winnerID   *int
	word       string
	startTime  *time.Time
	finishTime *time.Time
}

type Charades struct {
	s      bot.Service
	path   string
	data   map[string]map[string]string
	active map[int64]*act
}

func NewCharades(s bot.Service) *Charades {
	c := &Charades{
		s:      s,
		path:   infra.GetResourcesDir("charades"),
		data:   make(map[string]map[string]string),
		active: make(map[int64]*act),
	}
	return c
}

func (c *Charades) Handle(u *tgbotapi.Update, cm *db.ChatMeta) (proceed bool, err error) {
	if cm == nil {
		return true, nil
	}

	switch cm.Type {
	case "supergroup", "group", "private": // TODO remove private
		break
	default:
		return true, nil
	}

	b := c.s.GetBot()
	m := u.Message
	cb := u.CallbackQuery
	userAct := c.active[cm.ID]

	if cb != nil && isValidCharadeCallback(cb) {
		log.Trace("charade valid")
		answer := tgbotapi.CallbackConfig{
			CallbackQueryID: cb.ID,
		}

		if userAct != nil && cb.From.ID != userAct.userID {
			answer.Text = i18n.Get("It's not your turn!", cm.Language)
			b.Request(answer)
			return false, nil
		}

		switch cb.Data {
		case charadeShowWord:
			if userAct == nil {
				break
			}
			answer.Text = i18n.Get("It's not your turn!", cm.Language)
			if cb.From.ID == userAct.userID {
				answer.Text = userAct.word
				answer.ShowAlert = true
			}

		case charadeAnotherWord:
			if userAct == nil {
				break
			}
			answer.Text = i18n.Get("It's not your turn!", cm.Language)
			if cb.From.ID == userAct.userID {
				userAct, err := c.randomActForUser(cb.From.ID, cm)
				if err != nil {
					log.WithError(err).Error("cant get act")
				}
				c.active[cm.ID] = userAct
				answer.Text = userAct.word
				answer.ShowAlert = true
			}

		case charadeContinue:
			switch {
			case userAct == nil, userAct.userID == cb.From.ID, userAct.userID != cb.From.ID && time.Now().Unix()-(*userAct.finishTime).Unix() >= 5:
				err = c.startCharade(cb.From, cm)
				if err != nil {
					log.WithError(err).Error("cant start charade continue")
				}

			case userAct != nil && userAct.userID != cb.From.ID && time.Now().Unix()-(*userAct.finishTime).Unix() < 5:
				answer.Text = i18n.Get("It's not your turn!", cm.Language)
			}
		}
		b.Request(answer)
		return false, nil
	}

	if userAct != nil && m != nil && !m.IsCommand() {
		msg := tgbotapi.NewMessage(cm.ID, i18n.Get("Draw", cm.Language))
		msg.ParseMode = "markdown"
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(i18n.Get("I want to continue", cm.Language), charadeContinue),
			),
		)
		msg.ReplyMarkup = kb

		if strings.ToLower(strings.Trim(m.Text, " \n\r?!.,;!@#$%^&*()/][\\")) == userAct.word {
			if m.From.ID == userAct.userID {
			} else {
				msg.Text = i18n.Get("*%s makes the right guess*!", cm.Language)
				userAct.finishTime = func() *time.Time { t := time.Now(); return &t }()
			}

			b.Send(msg)

		}
	}

	if m != nil && m.IsCommand() && m.Command() == "charade" {
		log.Trace("charade trigger")
		if userAct != nil {
			userName, _ := bot.GetUN(m.From)
			b.Send(tgbotapi.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("Charade is in progress, go and win it, %s!", cm.Language), userName)))
			return false, nil
		}

		err = c.startCharade(m.From, cm)
		if err != nil {
			log.WithError(err).Error("cant start charade")
		}

		return false, nil
	}

	return true, nil
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

func (c *Charades) randomActForUser(userID int, cm *db.ChatMeta) (*act, error) {
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

	return &act{
		userID:    userID,
		word:      word,
		startTime: func() *time.Time { t := time.Now(); return &t }(),
	}, nil
}

func (c *Charades) startCharade(user *tgbotapi.User, cm *db.ChatMeta) error {
	userAct, err := c.randomActForUser(user.ID, cm)
	if err != nil {
		return errors.WithMessage(err, "cant get act")
	}

	c.active[cm.ID] = userAct
	userName, _ := bot.GetUN(user)
	msg := tgbotapi.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("You have one minute to explain word. Please, do not use sibling and other forms. Remember - both winner and explanator gets the point on success!\n\n*%s, press the button, to see the word, or to opt out.*", cm.Language), userName))
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
			log.WithError(err).Error("cant close reader")
		}
	}()

	res, err := ioutil.ReadAll(r)
	if err != nil {
		return errors.WithMessage(err, "cant read charades")
	}
	charades := make(map[string]string)
	if err = yaml.Unmarshal(res, &charades); err != nil {
		log.WithError(err).Errorln()
		return errors.WithMessage(err, "cant unmarshal charades")
	}

	c.data[lang] = charades
	return nil
}

func (c *Charades) open() {

}

func (c *Charades) getLogEntry() *log.Entry {
	return log.WithField("context", "charades")
}
