package handlers

import (
	"context"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/tool"
	log "github.com/sirupsen/logrus"
)

func (g *Gatekeeper) processNewChatMembers(ctx context.Context) error {
	entry := g.getLogEntry().WithField("method", "processNewChatMembers")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	recentJoiners, err := g.store.GetUnprocessedRecentJoiners(ctx)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get recent joiners")
		return err
	}
	if len(recentJoiners) > 0 {
		entry.WithField("count", len(recentJoiners)).Debug("processing new chat members")
	}
	for _, joiner := range recentJoiners {
		chatMember, err := g.s.GetBot().GetChatMember(api.GetChatMemberConfig{
			ChatConfigWithUser: api.ChatConfigWithUser{
				ChatConfig: api.ChatConfig{
					ChatID: joiner.ChatID,
				},
				UserID: joiner.UserID,
			},
		})
		if err != nil {
			if strings.Contains(err.Error(), "PARTICIPANT_ID_INVALID") {
				entry.WithFields(log.Fields{
					"user_id": joiner.UserID,
					"chat_id": joiner.ChatID,
					"reason":  "User not found in chat (left)",
				}).Info("Marking recent joiner as processed because they are no longer in the chat.")
				if err := g.store.ProcessRecentJoiner(ctx, joiner.ChatID, joiner.UserID, false); err != nil {
					entry.WithField("error", err.Error()).Error("failed to process recent joiner")
				}
			} else {
				entry.WithField("error", err.Error()).Error("failed to get chat member")
			}
			continue
		}

		if (chatMember == api.ChatMember{}) || chatMember.HasLeft() || chatMember.WasKicked() {
			entry.WithFields(log.Fields{
				"user_id": joiner.UserID,
				"chat_id": joiner.ChatID,
			}).Info("User has left or was kicked from the chat, marking as processed")
			if err := g.store.ProcessRecentJoiner(ctx, joiner.ChatID, joiner.UserID, false); err != nil {
				entry.WithField("error", err.Error()).Error("failed to process recent joiner")
			}
			continue
		}

		banned, err := g.banChecker.CheckBan(ctx, joiner.UserID)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to check ban")
			continue
		}
		if banned {
			entry.WithField("userID", joiner.UserID).Info("recent joiner is banned")
			banErr := bot.BanUserFromChat(ctx, g.s.GetBot(), joiner.UserID, joiner.ChatID, 0)
			if banErr != nil {
				entry.WithField("error", banErr.Error()).Error("failed to ban user")
			}
			if g.config.SpamControl.DebugUserID != 0 {
				errMsg := ""
				if banErr != nil {
					errMsg = banErr.Error()
				}
				debugMsg := tool.ExecTemplate(`Banned joiner: {{ .user_name }} ({{ .user_id }}){{ if .error }} {{ .error }}{{end}}`, map[string]any{
					"user_name": joiner.Username,
					"user_id":   joiner.UserID,
					"error":     errMsg,
				})
				_, _ = g.s.GetBot().Send(api.NewMessage(g.config.SpamControl.DebugUserID, debugMsg))
			}
			if banErr != nil {
				continue
			}
			if joiner.JoinMessageID != 0 {
				if err := bot.DeleteChatMessage(ctx, g.s.GetBot(), joiner.ChatID, joiner.JoinMessageID); err != nil {
					entry.WithField("error", err.Error()).Error("failed to delete join message")
				}
			}
		}
		if err := g.store.ProcessRecentJoiner(ctx, joiner.ChatID, joiner.UserID, banned); err != nil {
			entry.WithField("error", err.Error()).Error("failed to process recent joiner")
		}
	}

	return nil
}

func (g *Gatekeeper) processExpiredChallenges(ctx context.Context) error {
	entry := g.getLogEntry().WithField("method", "processExpiredChallenges")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	expired, err := g.store.GetExpiredChallenges(ctx, time.Now())
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get expired challenges")
		return err
	}
	for _, challenge := range expired {
		settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to load chat settings for challenge")
			continue
		}
		if !settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled {
			if err := g.cleanupChallengeWithoutPenalty(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to cleanup challenge without penalty")
			}
			continue
		}

		var targetChat *api.ChatFullInfo
		chat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
			ChatConfig: api.ChatConfig{
				ChatID: challenge.ChatID,
			},
		})
		if err != nil {
			entry.WithField("error", err.Error()).Error("cant get target chat info")
		} else {
			targetChat = &chat
		}

		language := g.s.GetLanguage(ctx, challenge.ChatID, nil)
		title := ""
		if targetChat != nil {
			title = targetChat.Title
		}
		rejectDuration, rejectText, err := g.rejectConfigFromSettings(settings, language, title)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to build reject config")
			continue
		}
		if err := g.failChallenge(ctx, challenge, rejectText, rejectDuration); err != nil {
			entry.WithField("error", err.Error()).Error("failed to process expired challenge")
		}
	}

	return nil
}
