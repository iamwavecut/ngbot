package handlers

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

type Punto struct {
	s    bot.Service
	path string
	data map[string][]string
}

const (
	ru = `!"№;%:?*()йцукенгшщзхъфывапролджэячсмитьбю.ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЯЧСМИТЬБЮ,Ёё`
	en = `!@#$%^&*()qwertyuiop[]asdfghjkl;'zxcvbnm,./QWERTYUIOP{}ASDFGHJKL:"ZXCVBNM<>?~` + "`"
)

var errNoFile = errors.New("no ngrams file for language")

func NewPunto(s bot.Service, path string) *Punto {
	p := &Punto{
		s:    s,
		path: path,
		data: map[string][]string{},
	}
	return p
}

func (p *Punto) Handle(u *api.Update, chat *api.Chat, user *api.User) (proceed bool, err error) {
	if chat == nil || user == nil {
		return true, nil
	}
	if user.IsBot {
		return true, nil
	}

	lang := p.getLanguage(chat, user)

	l := p.getLogEntry()
	if _, ok := p.data[lang]; !ok {
		err := p.load(lang)
		if err != nil {
			if err == errNoFile {
				return true, nil
			}
			l.WithError(err).Trace("cant load punto for lang " + lang)
			return true, nil // drop error
		}
	}

	switch chat.Type {
	case "supergroup", "group":
		break
	default:
		return true, nil
	}

	switch {
	case
		u.Message == nil,
		user.IsBot,
		u.Message.IsCommand():
		return true, nil
	}
	m := u.Message

	tokens := regexp.MustCompile(`[\s\-]+`).Split(m.Text, -1)
	i := 0
	for _, token := range tokens {
		for _, ngram := range p.data[lang] {
			if strings.Contains(" "+token+" ", ngram) {
				i++
			}
		}
	}
	l.Trace("i = " + strconv.Itoa(i))
	if i > 2 {
		pMessage, err := p.puntonize(m, lang)
		if err != nil {
			return true, nil // skip no mapping
		}

		nm := api.NewMessage(chat.ID, `^: `+pMessage)
		nm.ReplyParameters = api.ReplyParameters{
			MessageID:                m.MessageID,
			ChatID:                   chat.ID,
			AllowSendingWithoutReply: true,
		}
		nm.ParseMode = "markdown"

		_, err = p.s.GetBot().Send(nm)
		if err != nil {
			return true, errors.WithMessage(err, "cant send")
		}

	}

	return true, nil
}

func (p *Punto) puntonize(m *api.Message, lang string) (string, error) {
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

	mapping, ok := mappings[lang]
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

func (p *Punto) getLanguage(chat *api.Chat, user *api.User) string {
	if lang, err := p.s.GetDB().GetChatLanguage(chat.ID); !tool.Try(err) {
		return lang
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		return user.LanguageCode
	}
	return config.Get().DefaultLanguage
}
