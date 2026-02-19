package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
	"github.com/pborman/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const (
	greetingPlaceholderUser           = "{user}"
	greetingPlaceholderChatTitle      = "{chat_title}"
	greetingPlaceholderChatLinkTitled = "{chat_link_titled}"
	greetingPlaceholderTimeout        = "{timeout}"
)

func (g *Gatekeeper) handleNewChatMembersV2(ctx context.Context, u *api.Update, chat *api.Chat, settings *db.Settings) error {
	entry := g.getLogEntry().WithField("method", "handleNewChatMembersV2")

	if chat == nil {
		entry.Debug("chat is nil")
		return nil
	}
	if u == nil || u.Message == nil || u.Message.NewChatMembers == nil {
		entry.Debug("missing update message or chat members")
		return nil
	}
	if settings == nil {
		entry.Debug("settings are nil")
		return nil
	}
	if !settings.GatekeeperCaptchaEnabled && !settings.GatekeeperGreetingEnabled {
		entry.Debug("gatekeeper subfeatures are disabled")
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	var comm *api.ChatFullInfo
	chatInfo, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: chat.ID,
		},
	})
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get group chat info for gatekeeper")
	} else {
		comm = &chatInfo
	}

	for _, member := range u.Message.NewChatMembers {
		if g.banChecker.IsKnownBanned(member.ID) {
			entry.WithFields(log.Fields{
				"user_id": member.ID,
				"name":    bot.GetUN(&member),
			}).Info("recent joiner is known banned")
			if err := g.banChecker.BanUserWithMessage(ctx, chat.ID, member.ID, u.Message.MessageID); err != nil {
				entry.WithField("error", err.Error()).Error("failed to ban known banned user")
			}
			continue
		}

		recentJoiner := &db.RecentJoiner{
			UserID:        member.ID,
			ChatID:        chat.ID,
			JoinedAt:      time.Now(),
			JoinMessageID: u.Message.MessageID,
			Username:      bot.GetUN(&member),
			Processed:     false,
			IsSpammer:     false,
		}
		if _, err := g.store.AddChatRecentJoiner(ctx, recentJoiner); err != nil {
			entry.WithField("error", err.Error()).Error("failed to save recent joiner")
		}

		if member.IsBot || (!settings.GatekeeperCaptchaEnabled && !settings.GatekeeperGreetingEnabled) {
			continue
		}
		if comm == nil {
			entry.WithField("user_id", member.ID).Warn("skipping gatekeeper challenge due to missing chat info")
			continue
		}
		if err := g.handleJoin(ctx, u, []api.User{member}, chat, comm, settings); err != nil {
			entry.WithFields(log.Fields{
				"user_id": member.ID,
				"error":   err.Error(),
			}).Error("failed to handle gatekeeper flow for new member")
		}
	}

	return nil
}

func (g *Gatekeeper) handleChatJoinRequest(ctx context.Context, u *api.Update, settings *db.Settings) error {
	entry := g.getLogEntry().WithField("method", "handleChatJoinRequest")

	if u == nil || u.ChatJoinRequest == nil {
		entry.Debug("chat join request is nil")
		return nil
	}
	if settings == nil {
		entry.Debug("settings are nil")
		return nil
	}
	if !settings.GatekeeperCaptchaEnabled && !settings.GatekeeperGreetingEnabled {
		entry.Debug("both gatekeeper subfeatures are disabled")
		return nil
	}
	requiresDM := settings.GatekeeperCaptchaEnabled

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	target := &u.ChatJoinRequest.Chat
	comm, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	})
	if err != nil {
		if requiresDM {
			entry.WithField("error", err.Error()).Error("failed to get user private chat info")
			return err
		}
		entry.WithField("error", err.Error()).Warn("failed to get user private chat info for standalone greeting")
		return nil
	}
	if err := g.handleJoin(ctx, u, []api.User{u.ChatJoinRequest.From}, target, &comm, settings); err != nil {
		if requiresDM {
			return err
		}
		entry.WithField("error", err.Error()).Warn("failed to send standalone greeting for join request")
		return nil
	}
	return nil
}

func (g *Gatekeeper) handleJoin(ctx context.Context, u *api.Update, jus []api.User, target *api.Chat, comm *api.ChatFullInfo, settings *db.Settings) error {
	entry := g.getLogEntry().WithField("method", "handleJoin")

	if target == nil || comm == nil {
		return errors.New("target or comm chat is nil")
	}
	if settings == nil {
		return errors.New("settings are nil")
	}
	if !settings.GatekeeperCaptchaEnabled && !settings.GatekeeperGreetingEnabled {
		return nil
	}

	b := g.s.GetBot()
	challengeTimeout := settings.GetChallengeTimeout()
	captchaOptionsCount := normalizeCaptchaOptionsCount(settings.GatekeeperCaptchaOptionsCount)

	for _, ju := range jus {
		if ju.IsBot {
			continue
		}

		isPublic := comm.Chat.ID == target.ID
		if settings.GatekeeperCaptchaEnabled && isPublic {
			if _, err := b.Request(api.RestrictChatMemberConfig{
				ChatMemberConfig: api.ChatMemberConfig{
					ChatConfig: api.ChatConfig{
						ChatID: target.ID,
					},
					UserID: ju.ID,
				},
				UntilDate: time.Now().Add(challengeTimeout).Unix(),
				Permissions: &api.ChatPermissions{
					CanSendMessages:       false,
					CanSendAudios:         false,
					CanSendDocuments:      false,
					CanSendPhotos:         false,
					CanSendVideos:         false,
					CanSendVideoNotes:     false,
					CanSendVoiceNotes:     false,
					CanSendPolls:          false,
					CanSendOtherMessages:  false,
					CanAddWebPagePreviews: false,
					CanChangeInfo:         false,
					CanInviteUsers:        false,
					CanPinMessages:        false,
					CanManageTopics:       false,
				},
			}); err != nil {
				entry.WithField("error", err.Error()).Error("failed to restrict user")
			}
		}

		commLang := g.s.GetLanguage(ctx, comm.Chat.ID, &ju)
		greetingText := ""
		if settings.GatekeeperGreetingEnabled {
			greetingText = strings.TrimSpace(g.renderGreetingText(settings, &ju, target))
		}

		challengeText := ""
		var kb *api.InlineKeyboardMarkup
		var challenge *db.Challenge

		if settings.GatekeeperCaptchaEnabled {
			now := time.Now()
			challenge = &db.Challenge{
				CommChatID:  comm.Chat.ID,
				UserID:      ju.ID,
				ChatID:      target.ID,
				SuccessUUID: uuid.New(),
				CreatedAt:   now,
				ExpiresAt:   now.Add(challengeTimeout),
			}
			if u.Message != nil {
				challenge.JoinMessageID = u.Message.MessageID
			}
			if _, err := g.store.CreateChallenge(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to create challenge")
				return err
			}

			buttons, correctVariant := g.createCaptchaButtons(ju.ID, challenge.SuccessUUID, commLang, captchaOptionsCount)
			rows := captchaKeyboardRows(buttons)
			inlineRows := make([][]api.InlineKeyboardButton, 0, len(rows))
			for _, row := range rows {
				inlineRows = append(inlineRows, api.NewInlineKeyboardRow(row...))
			}
			markup := api.NewInlineKeyboardMarkup(inlineRows...)
			kb = &markup

			var keys []string
			if isPublic {
				keys = challengeKeys
			} else {
				keys = privateChallengeKeys
			}
			randomKey := keys[tool.RandInt(0, len(keys)-1)]
			nameString := fmt.Sprintf("[%s](tg://user?id=%d)", api.EscapeText(api.ModeMarkdown, bot.GetFullName(&ju)), ju.ID)

			args := []interface{}{nameString}
			if !isPublic {
				args = append(args, g.chatLinkTitled(target))
			}
			args = append(args, correctVariant[1])
			challengeText = fmt.Sprintf(i18n.Get(randomKey, commLang), args...)
		}

		msgText := composeGatekeeperMessage(greetingText, challengeText)
		if strings.TrimSpace(msgText) == "" {
			if challenge != nil {
				_ = g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID)
			}
			continue
		}

		msg := api.NewMessage(comm.Chat.ID, msgText)
		msg.ParseMode = api.ModeMarkdown
		if isPublic {
			msg.DisableNotification = true
		}
		if kb != nil {
			msg.ReplyMarkup = kb
		}

		sentMsg, err := b.Send(msg)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to send gatekeeper message")
			if challenge != nil {
				_ = g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID)
			}
			return errors.WithMessage(err, "cant send gatekeeper message")
		}

		if challenge != nil {
			challenge.ChallengeMessageID = sentMsg.MessageID
			if err := g.store.UpdateChallenge(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to update challenge message id")
			}
		}
	}

	return nil
}

func composeGatekeeperMessage(greetingText, challengeText string) string {
	greetingText = strings.TrimSpace(greetingText)
	challengeText = strings.TrimSpace(challengeText)

	switch {
	case greetingText != "" && challengeText != "":
		return greetingText + "\n\n" + challengeText
	case greetingText != "":
		return greetingText
	default:
		return challengeText
	}
}

func (g *Gatekeeper) renderGreetingText(settings *db.Settings, user *api.User, target *api.Chat) string {
	if settings == nil || user == nil || target == nil {
		return ""
	}
	template := strings.TrimSpace(settings.GatekeeperGreetingText)
	if template == "" {
		return ""
	}

	userPlaceholder := fmt.Sprintf(
		"[%s](tg://user?id=%d)",
		api.EscapeText(api.ModeMarkdown, bot.GetFullName(user)),
		user.ID,
	)
	titlePlaceholder := api.EscapeText(api.ModeMarkdown, target.Title)
	linkPlaceholder := g.chatLinkTitled(target)
	timeoutPlaceholder := humanizeDurationShort(settings.GetChallengeTimeout())

	template = strings.ReplaceAll(template, greetingPlaceholderUser, userPlaceholder)
	template = strings.ReplaceAll(template, greetingPlaceholderChatTitle, titlePlaceholder)
	template = strings.ReplaceAll(template, greetingPlaceholderChatLinkTitled, linkPlaceholder)
	template = strings.ReplaceAll(template, greetingPlaceholderTimeout, timeoutPlaceholder)

	return template
}

func (g *Gatekeeper) chatLinkTitled(target *api.Chat) string {
	if target == nil {
		return ""
	}
	escapedTitle := api.EscapeText(api.ModeMarkdown, target.Title)
	if target.UserName == "" {
		return escapedTitle
	}
	return fmt.Sprintf("[%s](https://t.me/%s)", escapedTitle, target.UserName)
}

func humanizeDurationShort(duration time.Duration) string {
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
