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
)

func (a *Admin) startPanelCleanup() {
	go func() {
		ticker := time.NewTicker(panelCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			a.cleanupExpiredPanels()
		}
	}()
}

func (a *Admin) cleanupExpiredPanels() {
	ctx := context.Background()
	before := time.Now().Add(-panelSessionTTL)
	sessions, err := a.s.GetDB().GetExpiredAdminPanelSessions(ctx, before)
	if err != nil {
		a.getLogEntry().WithField("error", err.Error()).Error("failed to load expired panel sessions")
		return
	}
	for _, session := range sessions {
		if session.MessageID != 0 {
			_ = bot.DeleteChatMessage(ctx, a.s.GetBot(), session.UserID, session.MessageID)
		}
		_ = a.s.GetDB().DeleteAdminPanelSession(ctx, session.ID)
	}
}

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
	return a.s.GetDB().SetChatBotMembership(ctx, membership)
}

func (a *Admin) handleSettingsCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	entry := a.getLogEntry().WithField("command", "settings")
	if msg == nil || chat == nil {
		return nil
	}
	if chat.Type != "group" && chat.Type != "supergroup" {
		return nil
	}
	if msg.SenderChat != nil || user == nil {
		_ = a.deleteGroupMessage(ctx, chat.ID, msg.MessageID)
		return nil
	}

	membership, err := a.s.GetDB().GetChatBotMembership(ctx, chat.ID)
	if err == nil && membership != nil && !membership.IsMember {
		return nil
	}

	stopTyping := a.startTyping(ctx, chat.ID)
	defer stopTyping()

	lang := a.s.GetLanguage(ctx, chat.ID, user)
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

	if msg.SenderChat != nil || !isManager(member) {
		_ = a.deleteGroupMessage(ctx, chat.ID, msg.MessageID)
		_ = a.deleteGroupMessage(ctx, chat.ID, placeholder.MessageID)
		return nil
	}

	if err := a.upsertManager(ctx, chat.ID, user.ID, member); err != nil {
		entry.WithField("error", err.Error()).Error("failed to upsert chat manager")
	}

	if err := a.s.GetDB().SetChatBotMembership(ctx, &db.ChatBotMembership{
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

	membership, err := a.s.GetDB().GetChatBotMembership(ctx, targetChatID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to load bot membership")
	}
	if membership == nil || !membership.IsMember {
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("Please run /settings in the group first", lang), nil)
		return nil
	}

	manager, err := a.s.GetDB().GetChatManager(ctx, targetChatID, user.ID)
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
	if !isManager(member) {
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
	session, err = a.s.GetDB().CreateAdminPanelSession(ctx, session)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to create admin panel session")
		_ = a.editMessage(ctx, chat.ID, placeholder.MessageID, i18n.Get("No access", lang), nil)
		return nil
	}
	state.SessionID = session.ID
	session.StateJSON = mustJSON(state)
	if err := a.s.GetDB().UpdateAdminPanelSession(ctx, session); err != nil {
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

	if command.Action == panelActionClose {
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

	session, err := a.s.GetDB().GetAdminPanelSessionByUserPage(ctx, user.ID, string(panelPageExamplePrompt))
	if err != nil {
		return true, err
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

	_, err = a.s.GetDB().CreateChatSpamExample(ctx, &db.ChatSpamExample{
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
}

func (a *Admin) handleGroupDeleteCallback(ctx context.Context, cq *api.CallbackQuery, user *api.User) error {
	payload := strings.TrimPrefix(cq.Data, "del_")
	encodedChatID, encodedMessageID, ok := splitDeletePayload(payload)
	if !ok {
		return nil
	}
	chatID, err := decodeChatID(encodedChatID)
	if err != nil {
		return nil
	}
	commandMessageID, err := decodeMessageID(encodedMessageID)
	if err != nil {
		return nil
	}

	member, err := a.getChatMember(ctx, chatID, user.ID)
	if err != nil {
		a.handleBotMembershipError(ctx, chatID, err)
		return nil
	}
	if !isPrivilegedModerator(member) {
		a.answerCallback(ctx, cq.ID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, user)))
		return nil
	}

	if cq.Message != nil {
		_ = a.deleteGroupMessage(ctx, chatID, cq.Message.MessageID)
	}
	_ = a.deleteGroupMessage(ctx, chatID, commandMessageID)
	a.answerCallback(ctx, cq.ID, "")
	return nil
}

func (a *Admin) applyPanelCommand(ctx context.Context, session *db.AdminPanelSession, state *panelState, command panelCommand) error {
	switch command.Action {
	case panelActionToggleFeature:
		return a.toggleFeature(ctx, session, state, command.Feature)
	case panelActionOpenLanguage:
		state.Page = panelPageLanguageList
	case panelActionLanguagePageNext:
		state.LanguagePage++
	case panelActionLanguagePagePrev:
		if state.LanguagePage > 0 {
			state.LanguagePage--
		}
	case panelActionSelectLanguage:
		if err := a.setChatLanguage(ctx, session.ChatID, command.Language); err != nil {
			return err
		}
		state.Language = command.Language
		state.Page = panelPageHome
	case panelActionOpenExamples:
		state.Page = panelPageExamplesList
	case panelActionExamplesPageNext:
		state.ListPage++
	case panelActionExamplesPagePrev:
		if state.ListPage > 0 {
			state.ListPage--
		}
	case panelActionAddExample:
		state.Page = panelPageExamplePrompt
		state.PromptError = ""
	case panelActionSelectExample:
		state.Page = panelPageExampleDetail
		state.SelectedExampleID = command.ExampleID
	case panelActionOpenDelete:
		state.Page = panelPageConfirmDelete
	case panelActionDeleteYes:
		if state.SelectedExampleID != 0 {
			if err := a.s.GetDB().DeleteChatSpamExample(ctx, state.SelectedExampleID); err != nil {
				return err
			}
		}
		state.Page = panelPageExamplesList
		state.SelectedExampleID = 0
	case panelActionDeleteNo:
		state.Page = panelPageExampleDetail
	case panelActionBack:
		switch state.Page {
		case panelPageLanguageList:
			state.Page = panelPageHome
		case panelPageExamplesList:
			state.Page = panelPageHome
		case panelPageExampleDetail:
			state.Page = panelPageExamplesList
		case panelPageExamplePrompt:
			state.Page = panelPageExamplesList
		case panelPageConfirmDelete:
			state.Page = panelPageExampleDetail
		default:
			state.Page = panelPageHome
		}
	case panelActionNoop:
	default:
	}
	return a.savePanelState(ctx, session, *state)
}

func (a *Admin) toggleFeature(ctx context.Context, session *db.AdminPanelSession, state *panelState, feature string) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	switch feature {
	case panelFeatureGatekeeper:
		settings.GatekeeperEnabled = !settings.GatekeeperEnabled
	case panelFeatureLLMFirst:
		settings.LLMFirstMessageEnabled = !settings.LLMFirstMessageEnabled
	case panelFeatureVoting:
		settings.CommunityVotingEnabled = !settings.CommunityVotingEnabled
	default:
		return nil
	}
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	state.Features = panelFeatureFlags{
		GatekeeperEnabled:      settings.GatekeeperEnabled,
		LLMFirstMessageEnabled: settings.LLMFirstMessageEnabled,
		CommunityVotingEnabled: settings.CommunityVotingEnabled,
	}
	return a.savePanelState(ctx, session, *state)
}

func (a *Admin) setChatLanguage(ctx context.Context, chatID int64, language string) error {
	settings, err := a.s.GetSettings(ctx, chatID)
	if err != nil {
		return err
	}
	settings.Language = language
	return a.s.SetSettings(ctx, settings)
}

func (a *Admin) loadPanelState(session *db.AdminPanelSession) (panelState, error) {
	state := panelState{}
	if err := json.Unmarshal([]byte(session.StateJSON), &state); err != nil {
		return state, err
	}
	return state, nil
}

func (a *Admin) savePanelState(ctx context.Context, session *db.AdminPanelSession, state panelState) error {
	session.Page = string(state.Page)
	session.StateJSON = mustJSON(state)
	session.UpdatedAt = time.Now()
	return a.s.GetDB().UpdateAdminPanelSession(ctx, session)
}

func newPanelState(userID int64, chatID int64, chatTitle string, settings *db.Settings) panelState {
	state := panelState{
		Page:         panelPageHome,
		ChatID:       chatID,
		UserID:       userID,
		ChatTitle:    chatTitle,
		Language:     settings.Language,
		ListPage:     0,
		LanguagePage: 0,
		Features: panelFeatureFlags{
			GatekeeperEnabled:      settings.GatekeeperEnabled,
			LLMFirstMessageEnabled: settings.LLMFirstMessageEnabled,
			CommunityVotingEnabled: settings.CommunityVotingEnabled,
		},
	}
	return state
}

func mustJSON(state panelState) string {
	data, err := json.Marshal(state)
	if err != nil {
		return "{}"
	}
	return string(data)
}

type panelCallbackCandidate struct {
	SessionID int64
	CommandID int64
}

func (a *Admin) findPanelSessionCommand(ctx context.Context, data string) (*db.AdminPanelSession, *db.AdminPanelCommand, bool, error) {
	candidates := parsePanelCallbackCandidates(data)
	if len(candidates) == 0 {
		return nil, nil, false, nil
	}
	for _, candidate := range candidates {
		session, err := a.s.GetDB().GetAdminPanelSession(ctx, candidate.SessionID)
		if err != nil {
			return nil, nil, true, err
		}
		if session == nil {
			continue
		}
		cmd, err := a.s.GetDB().GetAdminPanelCommand(ctx, candidate.CommandID)
		if err != nil {
			return nil, nil, true, err
		}
		if cmd == nil || cmd.SessionID != session.ID {
			continue
		}
		return session, cmd, true, nil
	}
	return nil, nil, true, nil
}

func parsePanelCallbackCandidates(data string) []panelCallbackCandidate {
	parts := strings.Split(data, "_")
	if len(parts) < 2 {
		return nil
	}
	candidates := make([]panelCallbackCandidate, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		sessionEnc := strings.Join(parts[:i], "_")
		commandEnc := strings.Join(parts[i:], "_")
		sessionID, err := decodeUint64Min(sessionEnc)
		if err != nil {
			continue
		}
		commandID, err := decodeUint64Min(commandEnc)
		if err != nil {
			continue
		}
		candidates = append(candidates, panelCallbackCandidate{
			SessionID: int64(sessionID),
			CommandID: int64(commandID),
		})
	}
	return candidates
}

func (a *Admin) replaceExistingSession(ctx context.Context, userID int64, chatID int64) error {
	session, err := a.s.GetDB().GetAdminPanelSessionByUserChat(ctx, userID, chatID)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	if session.MessageID != 0 {
		_ = bot.DeleteChatMessage(ctx, a.s.GetBot(), userID, session.MessageID)
	}
	return a.s.GetDB().DeleteAdminPanelSession(ctx, session.ID)
}

func (a *Admin) closePanelSession(ctx context.Context, session *db.AdminPanelSession) error {
	if session.MessageID != 0 {
		edit := api.NewEditMessageReplyMarkup(session.UserID, session.MessageID, api.InlineKeyboardMarkup{})
		_, _ = a.s.GetBot().Send(edit)
	}
	return a.s.GetDB().DeleteAdminPanelSession(ctx, session.ID)
}

func (a *Admin) ensureManagerAccess(ctx context.Context, chatID int64, userID int64, callbackID string) error {
	membership, err := a.s.GetDB().GetChatBotMembership(ctx, chatID)
	if err == nil && membership != nil && !membership.IsMember {
		if callbackID != "" {
			a.answerCallback(ctx, callbackID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, nil)))
		}
		return fmt.Errorf("bot is not member")
	}

	member, err := a.getChatMember(ctx, chatID, userID)
	if err != nil {
		a.handleBotMembershipError(ctx, chatID, err)
		if callbackID != "" {
			a.answerCallback(ctx, callbackID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, nil)))
		}
		return err
	}
	if !isManager(member) {
		if callbackID != "" {
			a.answerCallback(ctx, callbackID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, nil)))
		}
		return fmt.Errorf("no access")
	}
	return a.upsertManager(ctx, chatID, userID, member)
}

func (a *Admin) startTyping(ctx context.Context, chatID int64) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(panelTypingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = a.s.GetBot().Request(api.NewChatAction(chatID, api.ChatTyping))
			}
		}
	}()
	_, _ = a.s.GetBot().Request(api.NewChatAction(chatID, api.ChatTyping))
	return func() {
		close(stop)
	}
}

func (a *Admin) sendPlaceholder(ctx context.Context, chatID int64, language string) (api.Message, error) {
	msg := api.NewMessage(chatID, i18n.Get("Please wait...", language))
	msg.DisableNotification = true
	return a.s.GetBot().Send(msg)
}

func (a *Admin) editMessage(ctx context.Context, chatID int64, messageID int, text string, markup *api.InlineKeyboardMarkup) error {
	edit := api.NewEditMessageText(chatID, messageID, text)
	edit.ReplyMarkup = markup
	_, err := a.s.GetBot().Send(edit)
	return err
}

func (a *Admin) answerCallback(ctx context.Context, callbackID string, text string) {
	_, _ = a.s.GetBot().Request(api.NewCallback(callbackID, text))
}

func (a *Admin) getChatMember(ctx context.Context, chatID int64, userID int64) (*api.ChatMember, error) {
	member, err := a.s.GetBot().GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
	})
	if err != nil {
		return nil, err
	}
	return &member, nil
}

func (a *Admin) handleBotMembershipError(ctx context.Context, chatID int64, err error) {
	if err == nil {
		return
	}
	if isBotRemovedError(err) {
		_ = a.s.GetDB().SetChatBotMembership(ctx, &db.ChatBotMembership{
			ChatID:    chatID,
			IsMember:  false,
			UpdatedAt: time.Now(),
		})
	}
}

func (a *Admin) deleteGroupMessage(ctx context.Context, chatID int64, messageID int) error {
	err := bot.DeleteChatMessage(ctx, a.s.GetBot(), chatID, messageID)
	if isBotRemovedError(err) {
		_ = a.s.GetDB().SetChatBotMembership(ctx, &db.ChatBotMembership{
			ChatID:    chatID,
			IsMember:  false,
			UpdatedAt: time.Now(),
		})
	}
	return err
}

func (a *Admin) upsertManager(ctx context.Context, chatID int64, userID int64, member *api.ChatMember) error {
	manager := &db.ChatManager{
		ChatID:             chatID,
		UserID:             userID,
		CanManageChat:      member.CanManageChat,
		CanPromoteMembers:  member.CanPromoteMembers,
		CanRestrictMembers: member.CanRestrictMembers,
		UpdatedAt:          time.Now(),
	}
	return a.s.GetDB().UpsertChatManager(ctx, manager)
}

func isManager(member *api.ChatMember) bool {
	if member == nil {
		return false
	}
	if member.IsCreator() {
		return true
	}
	return member.IsAdministrator() && (member.CanManageChat || member.CanPromoteMembers)
}

func isPrivilegedModerator(member *api.ChatMember) bool {
	if member == nil {
		return false
	}
	if isManager(member) {
		return true
	}
	return member.IsAdministrator() && member.CanRestrictMembers
}

func isBotRemovedError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "forbidden") || strings.Contains(errText, "kicked") || strings.Contains(errText, "chat not found")
}

func (a *Admin) renderAndUpdatePanel(ctx context.Context, session *db.AdminPanelSession, state panelState, messageID int) error {
	text, markup, err := a.renderPanel(ctx, session, &state)
	if err != nil {
		return err
	}
	session.MessageID = messageID
	if err := a.savePanelState(ctx, session, state); err != nil {
		return err
	}
	if err := a.editMessage(ctx, session.UserID, messageID, text, markup); err != nil {
		msg, sendErr := a.s.GetBot().Send(api.NewMessage(session.UserID, text))
		if sendErr == nil {
			session.MessageID = msg.MessageID
			if err := a.savePanelState(ctx, session, state); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (a *Admin) renderPanel(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return "", nil, err
	}
	state.Features = panelFeatureFlags{
		GatekeeperEnabled:      settings.GatekeeperEnabled,
		LLMFirstMessageEnabled: settings.LLMFirstMessageEnabled,
		CommunityVotingEnabled: settings.CommunityVotingEnabled,
	}
	state.Language = settings.Language

	if err := a.s.GetDB().DeleteAdminPanelCommandsBySession(ctx, session.ID); err != nil {
		return "", nil, err
	}

	switch state.Page {
	case panelPageLanguageList:
		return a.renderLanguageList(ctx, session, state)
	case panelPageExamplesList:
		return a.renderExamplesList(ctx, session, state)
	case panelPageExampleDetail:
		return a.renderExampleDetail(ctx, session, state)
	case panelPageExamplePrompt:
		return a.renderExamplePrompt(ctx, session, state)
	case panelPageConfirmDelete:
		return a.renderConfirmDelete(ctx, session, state)
	default:
		return a.renderHome(ctx, session, state)
	}
}
