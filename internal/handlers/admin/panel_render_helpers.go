package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

const (
	panelGreetingPlaceholderUser           = "{user}"
	panelGreetingPlaceholderChatTitle      = "{chat_title}"
	panelGreetingPlaceholderChatLinkTitled = "{chat_link_titled}"
	panelGreetingPlaceholderTimeout        = "{timeout}"
)

func (a *Admin) commandButton(ctx context.Context, sessionID int64, label string, cmd panelCommand) (api.InlineKeyboardButton, error) {
	payload, err := a.createPanelCommand(ctx, sessionID, cmd)
	if err != nil {
		return api.InlineKeyboardButton{}, err
	}
	return api.NewInlineKeyboardButtonData(label, payload), nil
}

func (a *Admin) navRow(ctx context.Context, sessionID int64, prevAction string, nextAction string, hasPrev bool, hasNext bool) ([]api.InlineKeyboardButton, error) {
	row := make([]api.InlineKeyboardButton, 0, 3)
	if hasPrev {
		prevBtn, err := a.commandButton(ctx, sessionID, "⬅️", panelCommand{Action: prevAction})
		if err != nil {
			return nil, err
		}
		row = append(row, prevBtn)
	}
	backBtn, err := a.commandButton(ctx, sessionID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return nil, err
	}
	row = append(row, backBtn)
	if hasNext {
		nextBtn, err := a.commandButton(ctx, sessionID, "➡️", panelCommand{Action: nextAction})
		if err != nil {
			return nil, err
		}
		row = append(row, nextBtn)
	}
	return api.NewInlineKeyboardRow(row...), nil
}

func (a *Admin) createPanelCommand(ctx context.Context, sessionID int64, cmd panelCommand) (string, error) {
	payload, err := jsonMarshalCommand(cmd)
	if err != nil {
		return "", err
	}
	created, err := a.store.CreateAdminPanelCommand(ctx, &db.AdminPanelCommand{
		SessionID: sessionID,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
	if err != nil {
		return "", err
	}
	sessionEnc := encodeUint64Min(uint64(sessionID))
	commandEnc := encodeUint64Min(uint64(created.ID))
	return sessionEnc + "_" + commandEnc, nil
}

func jsonMarshalCommand(cmd panelCommand) (string, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func panelDurationLabel(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	if duration%time.Second == 0 {
		return fmt.Sprintf("%ds", int(duration/time.Second))
	}
	return duration.String()
}

func panelVotingTimeoutStateLabel(lang string, overrideNS int64) string {
	if overrideNS == int64(db.SettingsOverrideInherit) {
		return i18n.Get("Inherit", lang)
	}
	return panelDurationLabel(time.Duration(overrideNS))
}

func panelVotingIntStateLabel(lang string, value int, zeroNoCap bool) string {
	if value == db.SettingsOverrideInherit {
		return i18n.Get("Inherit", lang)
	}
	if zeroNoCap && value == 0 {
		return i18n.Get("No cap", lang)
	}
	return fmt.Sprintf("%d", value)
}

func panelSelectLabel(selected bool, label string) string {
	if selected {
		return "✅ " + label
	}
	return label
}

func panelHelpBlock(lang string, help string) string {
	return fmt.Sprintf("%s\n%s", i18n.Get("Help", lang), help)
}

func appendPanelHelp(base string, lang string, help string) string {
	return base + "\n\n" + panelHelpBlock(lang, help)
}

func renderGreetingPreviewQuote(state *panelState) string {
	if state == nil {
		return ""
	}
	text := strings.TrimSpace(db.StripGatekeeperGreetingTemplateSyntax(state.GatekeeperGreetingText))
	if text == "" {
		return ""
	}

	chatTitle := state.ChatTitle
	if chatTitle == "" {
		chatTitle = "Chat"
	}

	text = strings.ReplaceAll(text, panelGreetingPlaceholderUser, fmt.Sprintf("[User](tg://user?id=%d)", state.UserID))
	text = strings.ReplaceAll(text, panelGreetingPlaceholderChatTitle, chatTitle)
	text = strings.ReplaceAll(text, panelGreetingPlaceholderChatLinkTitled, chatTitle)
	text = strings.ReplaceAll(text, panelGreetingPlaceholderTimeout, panelDurationLabel(time.Duration(state.ChallengeTimeout)))

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

func chunkButtons(buttons []api.InlineKeyboardButton, perRow int) [][]api.InlineKeyboardButton {
	if len(buttons) == 0 {
		return nil
	}
	var rows [][]api.InlineKeyboardButton
	for i := 0; i < len(buttons); i += perRow {
		end := min(i+perRow, len(buttons))
		rows = append(rows, api.NewInlineKeyboardRow(buttons[i:end]...))
	}
	return rows
}

func languageLabel(code string) string {
	name := i18n.GetLanguageName(code)
	return fmt.Sprintf("%s (%s)", name, code)
}

func statusEmoji(enabled bool) string {
	if enabled {
		return "✅"
	}
	return "⬜"
}

func makePreview(text string, maxLen int) string {
	normalized := strings.ReplaceAll(text, "\n", " ")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	runes := []rune(normalized)
	if len(runes) <= maxLen {
		return normalized
	}
	return string(runes[:maxLen]) + "..."
}

func pageCount(total int, pageSize int) int {
	if total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func clampPage(page int, totalPages int) int {
	if totalPages <= 0 {
		return 0
	}
	if page < 0 {
		return 0
	}
	if page >= totalPages {
		return totalPages - 1
	}
	return page
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
