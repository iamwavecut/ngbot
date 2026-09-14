package handlers

import (
	"encoding/json"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
)

const (
	telegramEntityBotCommand  = "bot_command"
	telegramEntityMention     = "mention"
	telegramEntityTextMention = "text_mention"
)

func richMessageControls(message *api.RichMessage, self api.User) (command, mention bool) {
	if message == nil {
		return false, false
	}
	payload, err := json.Marshal(message.Blocks)
	if err != nil {
		return false, false
	}
	var blocks []any
	if err := json.Unmarshal(payload, &blocks); err != nil {
		return false, false
	}
	matchesUsername := func(value any) bool {
		username, _ := value.(string)
		return self.UserName != "" && strings.EqualFold(strings.TrimPrefix(username, "@"), self.UserName)
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, item := range node {
				visit(item)
			}
		case map[string]any:
			switch node["type"] {
			case telegramEntityBotCommand:
				command = true
			case telegramEntityMention:
				mention = mention || matchesUsername(node["username"])
			case telegramEntityTextMention:
				user, _ := node["user"].(map[string]any)
				id, _ := user["id"].(float64)
				mention = mention || (self.ID != 0 && id == float64(self.ID)) || matchesUsername(user["username"])
			}
			for _, item := range node {
				visit(item)
			}
		}
	}
	visit(blocks)
	return command, mention
}
