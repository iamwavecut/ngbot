package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
)

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
	return a.store.UpdateAdminPanelSession(ctx, session)
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
		session, err := a.store.GetAdminPanelSession(ctx, candidate.SessionID)
		if err != nil {
			return nil, nil, true, err
		}
		if session == nil {
			continue
		}
		cmd, err := a.store.GetAdminPanelCommand(ctx, candidate.CommandID)
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
	session, err := a.store.GetAdminPanelSessionByUserChat(ctx, userID, chatID)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	if session.MessageID != 0 {
		_ = bot.DeleteChatMessage(ctx, a.s.GetBot(), userID, session.MessageID)
	}
	return a.store.DeleteAdminPanelSession(ctx, session.ID)
}

func (a *Admin) closePanelSession(ctx context.Context, session *db.AdminPanelSession) error {
	if session.MessageID != 0 {
		_ = bot.DeleteChatMessage(ctx, a.s.GetBot(), session.UserID, session.MessageID)
	}
	return a.store.DeleteAdminPanelSession(ctx, session.ID)
}
