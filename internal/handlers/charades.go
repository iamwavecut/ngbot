package handlers

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
)

const (
	charadeShowWord    = "CHARADE_SHOW_WORD"
	charadeAnotherWord = "CHARADE_ANOTHER_WORD"
	charadeContinue    = "CHARADE_CONTINUE"

	charadeCommandStart = "charade"
	charadeCommandStats = "charating"
)

var (
	charadeCallbackData = []string{charadeShowWord, charadeAnotherWord, charadeContinue}
	charadeCommands     = []string{charadeCommandStart, charadeCommandStats}
)

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
	data   map[string][]string
	active map[int64]*actType
}

func NewCharades(s bot.Service) *Charades {
	c := &Charades{
		s:      s,
		path:   infra.GetResourcesDir("charades"),
		data:   make(map[string][]string),
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
				msg := api.NewMessage(chatID, fmt.Sprintf(i18n.Get("Nobody guessed the word *%s*, that is sad.", act.chatMeta.Language), act.word))
				msg.ParseMode = "markdown"
				appendContinueKeyboard(&msg, act.chatMeta.Language)
				_, _ = c.s.GetBot().Send(msg)
			}
		}
	}
}

func (c *Charades) Handle(u *api.Update, cm *db.ChatMeta, um *db.UserMeta) (bool, error) {
	if cm == nil || um == nil {
		return true, nil
	}

	switch cm.Type {
	case "supergroup", "group":
		break
	default:
		return true, nil
	}

	m := u.Message
	cb := u.CallbackQuery
	act := c.active[cm.ID]

	if um.IsBot {
		return true, nil
	}

	if cb != nil && isValidCharadeCallback(cb) {
		c.processCallback(cb, cm, um, act)
		return false, nil
	}

	if act != nil && m != nil && !m.IsCommand() {
		c.processAnswer(m, cm, um, act)
		return true, nil
	}

	if m != nil && m.IsCommand() && isValidCharadeCommand(m.Command()) {
		c.processCommand(m, cm, um, act)
		return false, nil
	}

	return true, nil
}

func (c *Charades) processCommand(m *api.Message, cm *db.ChatMeta, um *db.UserMeta, act *actType) {
	l := c.getLogEntry()
	l.Trace("charade trigger")
	b := c.s.GetBot()

	switch m.Command() {
	case charadeCommandStart:
		if act != nil {
			_, _ = b.Send(api.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("Charade is in progress, go and win it, %s!", cm.Language), bot.EscapeMarkdown(um.GetFullName()))))
			return
		}
		err := c.startCharade(um, cm)
		if err != nil {
			l.WithError(err).Error("cant start")
		}

	case charadeCommandStats:
		//stats, err := c.s.GetDB().GetCharadeStats(cm.ID)
		//if err != nil {
		//	l.WithError(err).Error("cant get stats")
		//}
	}
}

func (c *Charades) processAnswer(m *api.Message, cm *db.ChatMeta, um *db.UserMeta, act *actType) {
	l := c.getLogEntry()
	if act.finishTime != nil {
		return
	}
	msg := api.NewMessage(cm.ID, i18n.Get("Draw", cm.Language))
	msg.ParseMode = "markdown"
	appendContinueKeyboard(&msg, cm.Language)

	userWord := strings.ToLower(strings.Trim(m.Text, " \n\r?!.,;!@#$%^&*()/][\\"))
	userWord = strings.Replace(userWord, "ё", "е", -1)
	if userWord == strings.Replace(act.word, "ё", "е", -1) {
		if act.finishTime != nil {
			return
		}
		if um.ID != act.userID {
			msg.Text = fmt.Sprintf(i18n.Get("*%s* makes the right guess, smart pants!", cm.Language), bot.EscapeMarkdown(um.GetFullName()))
			msg.ReplyToMessageID = m.MessageID
			act.finishTime = func() *time.Time { t := time.Now(); return &t }()
			act.winnerID = func() *int { ID := um.ID; return &ID }()

			winnerScore, err := c.s.GetDB().AddCharadeScore(cm.ID, um.ID)
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

		_, _ = c.s.GetBot().Send(msg)
	}
}

func (c *Charades) processCallback(cb *api.CallbackQuery, cm *db.ChatMeta, um *db.UserMeta, act *actType) {
	l := c.getLogEntry()
	l.Trace("charade valid")
	answer := api.CallbackConfig{
		CallbackQueryID: cb.ID,
		Text:            i18n.Get("It's not your turn!", cm.Language),
	}

	switch cb.Data {

	case charadeShowWord:
		if act == nil || um.ID != act.userID {
			break
		}

		if um.ID == act.userID {
			answer.Text = act.word
			answer.ShowAlert = true
		}

	case charadeAnotherWord:
		if act == nil || um.ID != act.userID {
			break
		}

		if um.ID == act.userID {
			act, err := c.randomAct(um.ID, cm)
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
			act.winnerID != nil && *act.winnerID == um.ID,
			act.winnerID != nil && *act.winnerID != um.ID && time.Now().Unix()-(*act.finishTime).Unix() >= 10:

			err := c.startCharade(um, cm)
			if err != nil {
				l.WithError(err).Error("cant start charade continue")
			}
			answer.Text = ""

		case act != nil && act.winnerID != nil && *act.winnerID != um.ID && time.Now().Unix()-(*act.finishTime).Unix() < 10:
			answer.Text = i18n.Get("The winner has 10 seconds advantage, try later", cm.Language)
		}
	}
	_, _ = c.s.GetBot().Request(answer)
}

func (c *Charades) randomAct(userID int, cm *db.ChatMeta) (*actType, error) {
	if _, ok := c.data[cm.Language]; !ok {
		if err := c.load(cm.Language); err != nil {
			return nil, errors.WithMessagef(err, "cant load charades for %v", cm.Language)
		}
	}

	s := rand.NewSource(time.Now().Unix())
	r := rand.New(s) // initialize local pseudorandom generator
	wordIndex := r.Intn(len(c.data[cm.Language]))
	return &actType{
		userID:    userID,
		word:      c.data[cm.Language][wordIndex],
		startTime: func() *time.Time { t := time.Now(); return &t }(),
		chatMeta:  cm,
	}, nil
}

func (c *Charades) startCharade(um *db.UserMeta, cm *db.ChatMeta) error {
	act, err := c.randomAct(um.ID, cm)
	if err != nil {
		return errors.WithMessage(err, "cant get actType")
	}

	c.active[cm.ID] = act
	msg := api.NewMessage(cm.ID, fmt.Sprintf(i18n.Get("Please, *%s*, explain the word, without using synonyms and other forms in three minutes. Both the explainer and the winner get a _point_ on success!", cm.Language), bot.EscapeMarkdown(um.GetFullName())))
	msg.ParseMode = "markdown"
	kb := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonData(i18n.Get("💬 Show word", cm.Language), charadeShowWord),
			api.NewInlineKeyboardButtonData(i18n.Get("🔄 Replace word", cm.Language), charadeAnotherWord),
		),
	)
	msg.ReplyMarkup = kb
	_, _ = c.s.GetBot().Send(msg)

	return nil
}

func (c *Charades) load(lang string) error {
	l := c.getLogEntry()
	f, err := os.Open(filepath.Join(c.path, lang+".txt.gz"))
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

	scanner := bufio.NewScanner(r)
	charades := make([]string, 0, 25000)
	for scanner.Scan() {
		charades = append(charades, scanner.Text())
	}
	c.data[lang] = charades
	return nil
}

func (c *Charades) getLogEntry() *log.Entry {
	return log.WithField("context", "charades")
}

func appendContinueKeyboard(msg *api.MessageConfig, lang string) {
	kb := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonData(i18n.Get("I want to continue", lang), charadeContinue),
		),
	)
	msg.ReplyMarkup = kb
}

func isValidCharadeCallback(query *api.CallbackQuery) bool {
	var res bool
	for _, data := range charadeCallbackData {
		if data == query.Data {
			res = true
		}
	}
	return res
}

func isValidCharadeCommand(command string) bool {
	var res bool
	for _, data := range charadeCommands {
		if data == command {
			res = true
		}
	}
	return res
}
