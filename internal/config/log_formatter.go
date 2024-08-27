package config

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

type NbFormatter struct{}

func (f *NbFormatter) Format(entry *log.Entry) ([]byte, error) {
	const (
		red         = 31
		yellow      = 33
		blue        = 36
		gray        = 37
		green       = 32
		cyan        = 96
		lightYellow = 93
		lightGreen  = 92
	)
	levelColor := blue
	switch entry.Level {
	case 5, 6:
		levelColor = gray
	case 3:
		levelColor = yellow
	case 2, 1, 0:
		levelColor = red
	case 4:
		levelColor = blue
	}
	level := fmt.Sprintf(
		"\x1b[%dm%s\x1b[0m",
		levelColor,
		strings.ToUpper(entry.Level.String())[:4],
	)

	output := fmt.Sprintf("\x1b[%dm%s\x1b[0m=%s", cyan, "level", level)
	output += fmt.Sprintf(" \x1b[%dm%s\x1b[0m=\x1b[%dm%s\x1b[0m", cyan, "ts", lightYellow, entry.Time.Format("2006-01-02 15:04:05.000"))

	_, file, line, ok := runtime.Caller(6)
	if ok {
		output += fmt.Sprintf(" \x1b[%dm%s\x1b[0m=\x1b[%dm%s:%d\x1b[0m", cyan, "source", lightYellow, file, line)
	}

	for k, val := range entry.Data {
		var s string
		if m, err := json.Marshal(val); err == nil {
			s = string(m)
		}
		if s == "" {
			continue
		}
		valueColor := cyan
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			valueColor = green
		} else if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
			valueColor = lightYellow
		}
		output += fmt.Sprintf(" \x1b[%dm%s\x1b[0m=\x1b[%dm%s\x1b[0m", cyan, k, valueColor, s)
	}
	output += fmt.Sprintf(" \x1b[%dm%s\x1b[0m=\x1b[%dm\"%s\"\x1b[0m", cyan, "msg", lightGreen, entry.Message)
	output = strings.Replace(output, "\r", "\\r", -1)
	output = strings.Replace(output, "\n", "\\n", -1) + "\n"
	return []byte(output), nil
}
