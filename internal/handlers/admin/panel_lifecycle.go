package handlers

import (
	"context"
	"time"

	"github.com/iamwavecut/ngbot/internal/bot"
)

func (a *Admin) startPanelCleanup(ctx context.Context) {
	a.wg.Go(func() {
		ticker := time.NewTicker(panelCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.cleanupExpiredPanels(ctx)
			}
		}
	})
}

func (a *Admin) cleanupExpiredPanels(ctx context.Context) {
	before := time.Now().Add(-panelSessionTTL)
	sessions, err := a.store.GetExpiredAdminPanelSessions(ctx, before)
	if err != nil {
		a.getLogEntry().WithField("error", err.Error()).Error("failed to load expired panel sessions")
		return
	}
	for _, session := range sessions {
		if session.MessageID != 0 {
			_ = bot.DeleteChatMessage(ctx, a.s.GetBot(), session.UserID, session.MessageID)
		}
		_ = a.store.DeleteAdminPanelSession(ctx, session.ID)
	}
}
