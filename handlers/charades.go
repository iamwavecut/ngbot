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
)

const charadeShowWord = "CHARADE_SHOW_WORD"
const charadeOptOut = "CHARADE_OPT_OUT"

var charadeCallbackData = []string{charadeShowWord, charadeOptOut}

type act struct {
	userID int
	word   string
}

type Charades struct {
	s      bot.Service
	path   string
	data   map[string]map[string]string
	active map[int64]act
}

func NewCharades(s bot.Service) *Charades {
	c := &Charades{
		s:      s,
		path:   infra.GetResourcesDir("charades"),
		data:   make(map[string]map[string]string),
		active: make(map[int64]act),
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

	if u.Message == nil {
		return true, nil
	}
	m := u.Message
	b := c.s.GetBot()

	_, active := c.active[cm.ID]
	if m.IsCommand() && m.Command() == "charade" {
		log.Trace("charade trigger")
		if active {
			userName, _ := bot.GetUN(m.From)
			b.Send(tgbotapi.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("Charade is in progress, go and win it, %s!", cm.Language), userName)))
			return false, nil
		}

		err = c.startCharade(u, cm)
		if err != nil {
			log.WithError(err).Error("cant start charade")
		}

		return false, nil
	}

	if active {
		if u.CallbackQuery != nil && isValidCharadeCallback(u.CallbackQuery) {
			log.Trace("charade valid")
			answer := tgbotapi.CallbackConfig{
				CallbackQueryID: u.CallbackQuery.ID,
			}

			switch u.CallbackQuery.Data {
			case charadeShowWord:
				answer.Text = i18n.Get("It's not your turn!", cm.Language)
				if u.CallbackQuery.From.ID == c.active[cm.ID].userID {
					answer.Text = c.active[cm.ID].word
					answer.ShowAlert = true
				}

			case charadeOptOut:
				answer.Text = i18n.Get("It's not your turn!", cm.Language)
				if u.CallbackQuery.From.ID == c.active[cm.ID].userID {
					delete(c.active, cm.ID)
					answer.Text = i18n.Get("Ok then, you, coward.", cm.Language)
				}
			}

			b.Request(answer)
		}
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

func (c *Charades) startCharade(u *tgbotapi.Update, cm *db.ChatMeta) error {
	if _, ok := c.data[cm.Language]; !ok {
		if err := c.load(cm.Language); err != nil {
			return errors.WithMessage(err, "")
		}
	}

	wordIndex := rand.Intn(len(c.data[cm.Language]))
	var currentIndex int
	var word, description string
	for currentWord, currentDesc := range c.data[cm.Language] {
		if wordIndex == currentIndex {
			word = currentWord
			description = currentDesc
			break
		}
		currentIndex++
	}
	_ = description // todo define what to do with desc

	if word == "" {
		return errors.New("cant pick word")
	}

	c.active[cm.ID] = act{
		userID: u.Message.From.ID,
		word:   word,
	}
	userName, _ := bot.GetUN(u.Message.From)
	msg := tgbotapi.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("You have one minute to explain word. Please, do not use sibling and other forms. Remember - both winner and explanator gets the point on success!\n\n*%s, press the button, to see the word, or to opt out.*", cm.Language), userName))
	msg.ParseMode = "markdown"
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(i18n.Get("Show word", cm.Language), "charade_show_word"),
			tgbotapi.NewInlineKeyboardButtonData(i18n.Get("I'm a chicken", cm.Language), "charade_opt_out"),
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
