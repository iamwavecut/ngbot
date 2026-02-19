package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/policy/permissions"
)

func (a *Admin) handleMyChatMember(ctx context.Context, update *api.ChatMemberUpdated) error {
	if update == nil {
		return nil
	}
	status := update.NewChatMember.Status
	isMember := status != "left" && status != "kicked"
	membership := &db.ChatBotMembership{
		ChatID:    update.Chat.ID,
		IsMember:  isMember,
		UpdatedAt: time.Now(),
	}
	return a.store.SetChatBotMembership(ctx, membership)
}

func (a *Admin) handleSettingsCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	entry := a.getLogEntry().WithField("command", "settings")
	if msg == nil || chat == nil || user == nil {
		return nil
	}
	lang := a.s.GetLanguage(ctx, chat.ID, user)
	if chat.Type == "private" {
		reply := api.NewMessage(chat.ID, i18n.Get("Please run /settings in the group first", lang))
		reply.DisableNotification = true
		_, _ = a.s.GetBot().Send(reply)
		return nil
	}
	if chat.Type != "group" && chat.Type != "supergroup" {
		return nil
	}
	if msg.SenderChat != nil {
		_ = a.deleteGroupMessage(ctx, chat.ID, msg.MessageID)
		return nil
	}

	membership, err := a.store.GetChatBotMembership(ctx, chat.ID)
	if err == nil && membership != nil && !membership.IsMember {
		return nil
	}

	stopTyping := a.startTyping(ctx, chat.ID)
	defer stopTyping()

	placeholder, err := a.sendPlaceholder(ctx, chat.ID, lang)
	if err != nil {
		a.handleBotMembershipError(ctx, chat.ID, err)
		return nil
	}

	member, err := a.getChatMember(ctx, chat.ID, user.ID)
	if err != nil {
		a.handleBotMembershipError(ctx, chat.ID, err)
		_ = a.deleteGroupMessage(ctx, chat.ID, msg.MessageID)
		_ = a.deleteGroupMessage(ctx, chat.ID, placeholder.MessageID)
		return nil
	}

	if msg.SenderChat != nil || !permissions.IsManager(member) {
		_ = a.deleteGroupMessage(ctx, chat.ID, msg.MessageID)
		_ = a.deleteGroupMessage(ctx, chat.ID, placeholder.MessageID)
		return nil
	}

	if err := a.upsertManager(ctx, chat.ID, user.ID, member); err != nil {
		entry.WithField("error", err.Error()).Error("failed to upsert chat manager")
	}

	if err := a.store.SetChatBotMembership(ctx, &db.ChatBotMembership{
		ChatID:    chat.ID,
		IsMember:  true,
		UpdatedAt: time.Now(),
	}); err != nil {
		entry.WithField("error", err.Error()).Error("failed to set chat bot membership")
	}

	encodedChatID := encodeChatID(chat.ID)
	link := fmt.Sprintf("https://t.me/%s?start=settings_%s", a.s.GetBot().Self.UserName, encodedChatID)
	text := i18n.Get("Open settings in private chat", lang)

	delPayload := fmt.Sprintf("del_%s_%s", encodedChatID, encodeMessageID(msg.MessageID))
	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonURL(i18n.Get("Open Settings", lang), link),
			api.NewInlineKeyboardButtonData("❌", delPayload),
		),
	)

	edit := api.NewEditMessageText(chat.ID, placeholder.MessageID, text)
	edit.ReplyMarkup = &keyboard
	if _, err := a.s.GetBot().Send(edit); err != nil {
		a.handleBotMembershipError(ctx, chat.ID, err)
		return nil
	}
	return nil
}

func (a *Admin) handleStartSettings(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, payload string) error {
	entry := a.getLogEntry().WithField("command", "start_settings")
	if msg == nil || chat == nil || user == nil {
		return nil
	}
	if chat.Type != "private" {
		return nil
	}
	if !strings.HasPrefix(payload, "settings_") {
		return nil
	}

	stopTyping := a.startTyping(ctx, chat.ID)
	defer stopTyping()

	lang := a.s.GetLanguage(ctx, chat.ID, user)
	placeholder, err := a.sendPlaceholder(ctx, chat.ID, lang)
	if err != nil {
		return nil
	}

	encodedChatID := strings.TrimPrefix(payload, "settings_")
	targetChatID, err := decodeChatID(encodedChatID)
	if err != nil {
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}

	membership, err := a.store.GetChatBotMembership(ctx, targetChatID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to load bot membership")
	}
	if membership == nil || !membership.IsMember {
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("Please run /settings in the group first", lang), nil)
		return nil
	}

	manager, err := a.store.GetChatManager(ctx, targetChatID, user.ID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to load chat manager")
	}
	if manager == nil {
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}

	member, err := a.getChatMember(ctx, targetChatID, user.ID)
	if err != nil {
		a.handleBotMembershipError(ctx, targetChatID, err)
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}
	if !permissions.IsManager(member) {
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}

	if err := a.upsertManager(ctx, targetChatID, user.ID, member); err != nil {
		entry.WithField("error", err.Error()).Error("failed to upsert chat manager")
	}

	if err := a.replaceExistingSession(ctx, user.ID, targetChatID); err != nil {
		entry.WithField("error", err.Error()).Error("failed to replace existing session")
	}

	settings, err := a.s.GetSettings(ctx, targetChatID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to load settings")
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}

	chatTitle := ""
	targetChat, err := a.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: targetChatID,
		},
	})
	if err == nil {
		chatTitle = targetChat.Title
	} else {
		entry.WithField("error", err.Error()).Error("failed to get target chat")
	}

	state := newPanelState(user.ID, targetChatID, chatTitle, settings)
	session := &db.AdminPanelSession{
		UserID:    user.ID,
		ChatID:    targetChatID,
		Page:      string(state.Page),
		StateJSON: mustJSON(state),
		MessageID: placeholder.MessageID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	session, err = a.store.CreateAdminPanelSession(ctx, session)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to create admin panel session")
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}
	state.SessionID = session.ID
	session.StateJSON = mustJSON(state)
	if err := a.store.UpdateAdminPanelSession(ctx, session); err != nil {
		entry.WithField("error", err.Error()).Error("failed to update admin panel session")
	}

	return a.renderAndUpdatePanel(ctx, session, state, placeholder.MessageID)
}

func (a *Admin) handlePanelCallback(ctx context.Context, cq *api.CallbackQuery, chat *api.Chat, user *api.User) (bool, error) {
	if cq == nil || user == nil {
		return false, nil
	}
	if strings.HasPrefix(cq.Data, "del_") {
		return true, a.handleGroupDeleteCallback(ctx, cq, user)
	}

	session, cmd, ok, err := a.findPanelSessionCommand(ctx, cq.Data)
	if err != nil {
		return true, err
	}
	if !ok {
		return false, nil
	}
	if session == nil || cmd == nil {
		lang := "en"
		if cq.Message != nil {
			lang = a.s.GetLanguage(ctx, cq.Message.Chat.ID, user)
		}
		a.answerCallback(ctx, cq.ID, i18n.Get("Session expired", lang))
		return true, nil
	}

	if session.UserID != user.ID {
		a.answerCallback(ctx, cq.ID, i18n.Get("No access", a.s.GetLanguage(ctx, session.ChatID, user)))
		return true, nil
	}

	stopTyping := a.startTyping(ctx, session.UserID)
	defer stopTyping()

	state, err := a.loadPanelState(session)
	if err != nil {
		return true, err
	}

	command := panelCommand{}
	if err := json.Unmarshal([]byte(cmd.Payload), &command); err != nil {
		return true, err
	}

	if command.Action == panelActionCloseConfirm {
		a.answerCallback(ctx, cq.ID, "")
		return true, a.closePanelSession(ctx, session)
	}

	if err := a.ensureManagerAccess(ctx, session.ChatID, user.ID, cq.ID); err != nil {
		return true, err
	}

	if err := a.applyPanelCommand(ctx, session, &state, command); err != nil {
		return true, err
	}

	a.answerCallback(ctx, cq.ID, "")
	return true, a.renderAndUpdatePanel(ctx, session, state, session.MessageID)
}

func (a *Admin) handlePanelInput(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) (bool, error) {
	if msg == nil || chat == nil || user == nil {
		return false, nil
	}
	if chat.Type != "private" {
		return false, nil
	}
	if msg.Text == "" || msg.IsCommand() {
		return false, nil
	}

	session, err := a.store.GetAdminPanelSessionByUserPage(ctx, user.ID, string(panelPageExamplePrompt))
	if err != nil {
		return true, err
	}
	if session == nil {
		session, err = a.store.GetAdminPanelSessionByUserPage(ctx, user.ID, string(panelPageGatekeeperGreetingPrompt))
		if err != nil {
			return true, err
		}
	}
	if session == nil {
		return false, nil
	}

	stopTyping := a.startTyping(ctx, chat.ID)
	defer stopTyping()

	state, err := a.loadPanelState(session)
	if err != nil {
		return true, err
	}

	if err := a.ensureManagerAccess(ctx, session.ChatID, user.ID, ""); err != nil {
		return true, err
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" || len([]rune(text)) > panelMaxInputLen {
		state.PromptError = i18n.Get("Invalid input", state.Language)
		if err := a.savePanelState(ctx, session, state); err != nil {
			return true, err
		}
		return true, a.renderAndUpdatePanel(ctx, session, state, session.MessageID)
	}

	switch panelPage(session.Page) {
	case panelPageExamplePrompt:
		_, err = a.store.CreateChatSpamExample(ctx, &db.ChatSpamExample{
			ChatID:          session.ChatID,
			Text:            text,
			CreatedByUserID: user.ID,
			CreatedAt:       time.Now(),
		})
		if err != nil {
			return true, err
		}

		if session.MessageID != 0 {
			_ = bot.DeleteChatMessage(ctx, a.s.GetBot(), user.ID, session.MessageID)
		}

		state.Page = panelPageExamplesList
		state.ListPage = 0
		state.PromptError = ""
		state.SelectedExampleID = 0

		newMsg, err := a.sendPlaceholder(ctx, user.ID, state.Language)
		if err != nil {
			return true, err
		}
		session.MessageID = newMsg.MessageID
		if err := a.savePanelState(ctx, session, state); err != nil {
			return true, err
		}
		return true, a.renderAndUpdatePanel(ctx, session, state, session.MessageID)
	case panelPageGatekeeperGreetingPrompt:
		settings, err := a.s.GetSettings(ctx, session.ChatID)
		if err != nil {
			return true, err
		}
		settings.GatekeeperGreetingText = text
		if err := a.s.SetSettings(ctx, settings); err != nil {
			return true, err
		}

		state.GatekeeperGreetingText = text
		state.Page = panelPageGatekeeper
		state.PromptError = ""
		if err := a.savePanelState(ctx, session, state); err != nil {
			return true, err
		}
		return true, a.renderAndUpdatePanel(ctx, session, state, session.MessageID)
	default:
		return false, nil
	}
}
