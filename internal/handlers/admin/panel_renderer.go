package handlers

import (
	"context"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

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
		if isMessageNotModifiedError(err) {
			return nil
		}
		msg := api.NewMessage(session.UserID, text)
		msg.DisableNotification = true
		msg.ReplyMarkup = markup
		sent, sendErr := a.s.GetBot().Send(msg)
		if sendErr == nil {
			session.MessageID = sent.MessageID
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

	if err := a.store.DeleteAdminPanelCommandsBySession(ctx, session.ID); err != nil {
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
	case panelPageConfirmClose:
		return a.renderConfirmClose(ctx, session, state)
	default:
		return a.renderHome(ctx, session, state)
	}
}

func pageForLanguage(code string, all []string, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	for i, item := range all {
		if item == code {
			return i / pageSize
		}
	}
	return 0
}

func isMessageNotModifiedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}
