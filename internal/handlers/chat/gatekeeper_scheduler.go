package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
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

		isNotSpammer, err := g.store.IsChatNotSpammer(ctx, joiner.ChatID, joiner.UserID, joiner.Username)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to check manual not-spammer override")
			continue
		}
		if isNotSpammer {
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
		if challenge.Status == db.ChallengeStatusPassedWaitingMemberJoin {
			if err := g.cleanupChallengeWithoutPenalty(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to cleanup non-punitive challenge state")
			}
			continue
		}
		if challenge.Status == db.ChallengeStatusWebAppFallbackPending {
			if err := g.fallbackClaimedWebAppChallenge(ctx, challenge, settings); err != nil {
				entry.WithField("error", err.Error()).Error("failed to recover stuck web app fallback challenge")
			}
			continue
		}
		if challenge.WebAppToken != "" && challenge.JoinRequestQueryID != "" {
			if err := g.attemptWebAppFallback(ctx, challenge, settings); err != nil {
				entry.WithField("error", err.Error()).Error("failed to fallback expired web app challenge")
			}
			continue
		}
		if challenge.CommChatID != challenge.ChatID && challenge.JoinRequestQueryID == "" {
			if err := g.cleanupChallengeWithoutPenalty(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to cleanup non-punitive challenge state")
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
		if challenge.CommChatID != challenge.ChatID {
			language = g.dmLanguage(challenge.UserLanguage, nil)
		}
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

func (g *Gatekeeper) attemptWebAppFallback(ctx context.Context, challenge *db.Challenge, settings *db.Settings) error {
	claimed, err := g.store.ClaimWebAppChallengeForFallback(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	challenge.Status = db.ChallengeStatusWebAppFallbackPending
	return g.fallbackClaimedWebAppChallenge(ctx, challenge, settings)
}

func (g *Gatekeeper) fallbackClaimedWebAppChallenge(ctx context.Context, challenge *db.Challenge, settings *db.Settings) error {
	privateChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: challenge.CommChatID}})
	if err != nil {
		return errors.Join(fmt.Errorf("get private chat for fallback: %w", err), g.declineWebAppChallenge(ctx, challenge))
	}
	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: challenge.ChatID}})
	if err != nil {
		return errors.Join(fmt.Errorf("get target chat for fallback: %w", err), g.declineWebAppChallenge(ctx, challenge))
	}
	user := &api.User{
		ID:           challenge.UserID,
		FirstName:    privateChat.FirstName,
		LastName:     privateChat.LastName,
		UserName:     privateChat.UserName,
		LanguageCode: challenge.UserLanguage,
	}
	if user.FirstName == "" && user.UserName == "" {
		user.FirstName = "friend"
	}
	if err := g.startChallenge(ctx, nil, user, &targetChat.Chat, challenge.CommChatID, challenge.CommChatID, settings); err != nil {
		return errors.Join(fmt.Errorf("start dm fallback challenge: %w", err), g.declineWebAppChallenge(ctx, challenge))
	}
	fallbackChallenge, err := g.store.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		return errors.Join(fmt.Errorf("load dm fallback challenge: %w", err), g.declineWebAppChallenge(ctx, challenge))
	}
	if fallbackChallenge == nil || fallbackChallenge.CommChatID != challenge.CommChatID {
		return g.declineWebAppChallenge(ctx, challenge)
	}
	fallbackChallenge.JoinRequestQueryID = challenge.JoinRequestQueryID
	return g.store.UpdateChallenge(ctx, fallbackChallenge)
}

func (g *Gatekeeper) processUnopenedWebAppChallenges(ctx context.Context) error {
	entry := g.getLogEntry().WithField("method", "processUnopenedWebAppChallenges")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	deadline := time.Now().Add(-webAppOpenDeadline)
	unopened, err := g.store.GetUnopenedWebAppChallenges(ctx, deadline)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get unopened web app challenges")
		return err
	}
	for _, challenge := range unopened {
		settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to load chat settings for unopened challenge")
			continue
		}
		if !settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled {
			if err := g.declineWebAppChallenge(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to clean up unopened challenge for disabled gatekeeper")
			}
			continue
		}
		if err := g.attemptWebAppFallback(ctx, challenge, settings); err != nil {
			entry.WithField("error", err.Error()).Error("failed to fall back unopened web app challenge")
		}
	}
	return nil
}
