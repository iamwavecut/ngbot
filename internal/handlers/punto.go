package handlers

import (
	"bufio"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Punto struct {
	s          bot.Service
	path       string
	data       map[string][]string
	exclusions map[string][]string
}

var errNoFile = errors.New("no ngrams file for language")

func NewPunto(s bot.Service, path string) *Punto {
	p := &Punto{
		s:    s,
		path: path,
		data: map[string][]string{},
	}
	return p
}

func (p *Punto) Handle(u *tgbotapi.Update, cm *db.ChatMeta, um *db.UserMeta) (proceed bool, err error) {
	if cm == nil || um == nil {
		return true, nil
	}

	l := p.getLogEntry()
	if _, ok := p.data[cm.Language]; !ok {
		err := p.load(cm.Language)
		if err != nil {
			if err == errNoFile {
				return true, nil
			}
			l.WithError(err).Trace("cant load punto for lang " + cm.Language)
			return true, nil // drop error
		}
	}

	if um.IsBot {
		return true, nil
	}

	switch cm.Type {
	case "supergroup", "group":
		break
	default:
		return true, nil
	}

	switch {
	case
		u.Message == nil,
		um.IsBot,
		u.Message.IsCommand():
		return true, nil
	}
	m := u.Message

	tokens := regexp.MustCompile(`[\s\-]+`).Split(m.Text, -1)
	i := 0
	for _, token := range tokens {
		for _, ngram := range p.data[cm.Language] {
			if strings.Contains(" "+token+" ", ngram) {
				i++
			}
		}
	}
	l.Trace("i = " + string(i))
	if i > 2 {
		pMessage, err := p.puntonize(m, cm)
		if err != nil {
			return true, nil // skip no mapping
		}

		nm := tgbotapi.NewMessage(cm.ID, `^: `+pMessage)
		nm.ReplyToMessageID = m.MessageID
		nm.ParseMode = "markdown"

		_, err = p.s.GetBot().Send(nm)
		if err != nil {
			return true, errors.WithMessage(err, "cant send")
		}

	}

	return true, nil
}

func (p *Punto) puntonize(m *tgbotapi.Message, cm *db.ChatMeta) (string, error) {
	ru := `!"№;%:?*()йцукенгшщзхъфывапролджэячсмитьбю.ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЯЧСМИТЬБЮ,Ёё`
	en := `!@#$%^&*()qwertyuiop[]asdfghjkl;'zxcvbnm,./QWERTYUIOP{}ASDFGHJKL:"ZXCVBNM<>?~` + "`"
	mappings := map[string][2][]rune{
		"ru": {
			[]rune(ru),
			[]rune(en),
		},
		"en": {
			[]rune(en),
			[]rune(ru),
		},
	}

	mapping, ok := mappings[cm.Language]
	if !ok {
		return "", errors.New("no mapping")
	}

	res := m.Text
	for i := range mapping[0] {
		res = strings.ReplaceAll(res, string(mapping[1][i]), string(mapping[0][i]))
	}

	return res, nil
}

func (p *Punto) getLogEntry() *log.Entry {
	return log.WithField("context", "punto")
}

func (p *Punto) load(lang string) error {
	f, err := os.Open(filepath.Join(p.path, lang+".txt"))
	if err != nil {
		return errors.WithMessagef(errNoFile, "cant open charades lang %s", lang)
	}

	scanner := bufio.NewScanner(f)
	ngrams := make([]string, 0, 10000)
	for scanner.Scan() {
		ngrams = append(ngrams, scanner.Text())
	}
	p.data[lang] = ngrams
	return nil
}
