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
		shortFile := file[strings.LastIndex(file, "/")+1:]
		output += fmt.Sprintf(" \x1b[%dm%s\x1b[0m=\x1b[%dm%s:%d\x1b[0m", cyan, "source", lightYellow, shortFile, line)
	}

	for k, val := range entry.Data {
		s := formatLogValue(val)
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
	output = strings.ReplaceAll(output, "\r", "\\r")
	output = strings.ReplaceAll(output, "\n", "\\n") + "\n"
	return []byte(Redact(output)), nil
}

func formatLogValue(value any) string {
	switch value := value.(type) {
	case error:
		encoded, _ := json.Marshal(value.Error())
		return string(encoded)
	case fmt.Stringer:
		encoded, _ := json.Marshal(value.String())
		return string(encoded)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
