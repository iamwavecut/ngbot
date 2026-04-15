package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
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

	for _, member := range u.Message.NewChatMembers {
		isNotSpammer, err := g.store.IsChatNotSpammer(ctx, chat.ID, member.ID, member.UserName)
		if err != nil {
			entry.WithFields(log.Fields{
				"user_id": member.ID,
				"error":   err.Error(),
			}).Error("failed to check manual not-spammer override")
			continue
		}

		if !isNotSpammer && g.banChecker.IsKnownBanned(member.ID) {
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
			Username:      member.UserName,
			Processed:     false,
			IsSpammer:     false,
		}
		if _, err := g.store.AddChatRecentJoiner(ctx, recentJoiner); err != nil {
			entry.WithField("error", err.Error()).Error("failed to save recent joiner")
		}

		if member.IsBot || (!settings.GatekeeperCaptchaEnabled && !settings.GatekeeperGreetingEnabled) {
			continue
		}

		challenge, err := g.store.GetChallengeByChatUser(ctx, chat.ID, member.ID)
		if err != nil {
			entry.WithFields(log.Fields{
				"user_id": member.ID,
				"error":   err.Error(),
			}).Error("failed to load existing challenge by chat and user")
		}
		if challenge != nil && challenge.Status == db.ChallengeStatusPassedWaitingMemberJoin {
			if err := g.sendGreeting(ctx, chat.ID, chat, &member, settings, true); err != nil {
				entry.WithFields(log.Fields{
					"user_id": member.ID,
					"error":   err.Error(),
				}).Error("failed to send gatekeeper greeting after approved join request")
			}
			if err := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); err != nil {
				entry.WithFields(log.Fields{
					"user_id": member.ID,
					"error":   err.Error(),
				}).Error("failed to delete approved join request handoff challenge")
			}
			continue
		}

		switch {
		case settings.GatekeeperCaptchaEnabled:
			if err := g.startChallenge(ctx, u, &member, chat, chat.ID, chat.ID, settings); err != nil {
				entry.WithFields(log.Fields{
					"user_id": member.ID,
					"error":   err.Error(),
				}).Error("failed to handle gatekeeper captcha for new member")
			}
		case settings.GatekeeperGreetingEnabled:
			if err := g.sendGreeting(ctx, chat.ID, chat, &member, settings, true); err != nil {
				entry.WithFields(log.Fields{
					"user_id": member.ID,
					"error":   err.Error(),
				}).Error("failed to send gatekeeper greeting for new member")
			}
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
	if !settings.GatekeeperCaptchaEnabled {
		entry.Debug("captcha is disabled for join requests, leaving request for manual review")
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if _, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	}); err != nil {
		entry.WithField("error", err.Error()).Error("failed to get user private chat info")
		return err
	}

	return g.startChallenge(
		ctx,
		u,
		&u.ChatJoinRequest.From,
		&u.ChatJoinRequest.Chat,
		u.ChatJoinRequest.UserChatID,
		u.ChatJoinRequest.UserChatID,
		settings,
	)
}

func (g *Gatekeeper) startChallenge(ctx context.Context, u *api.Update, user *api.User, target *api.Chat, recipientChatID int64, languageChatID int64, settings *db.Settings) error {
	entry := g.getLogEntry().WithField("method", "startChallenge")

	if user == nil || target == nil {
		return errors.New("user or target chat is nil")
	}
	if settings == nil {
		return errors.New("settings are nil")
	}
	if !settings.GatekeeperCaptchaEnabled || user.IsBot {
		return nil
	}

	b := g.s.GetBot()
	challengeTimeout := settings.GetChallengeTimeout()
	captchaOptionsCount := normalizeCaptchaOptionsCount(settings.GatekeeperCaptchaOptionsCount)
	isPublic := recipientChatID == target.ID

	if isPublic {
		if _, err := b.Request(api.RestrictChatMemberConfig{
			ChatMemberConfig: api.ChatMemberConfig{
				ChatConfig: api.ChatConfig{
					ChatID: target.ID,
				},
				UserID: user.ID,
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

	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:  recipientChatID,
		UserID:      user.ID,
		ChatID:      target.ID,
		Status:      db.ChallengeStatusPending,
		SuccessUUID: uuid.New(),
		CreatedAt:   now,
		ExpiresAt:   now.Add(challengeTimeout),
	}
	if u != nil && u.Message != nil {
		challenge.JoinMessageID = u.Message.MessageID
	}
	if _, err := g.store.CreateChallenge(ctx, challenge); err != nil {
		entry.WithField("error", err.Error()).Error("failed to create challenge")
		return err
	}
	if err := handlersbase.IncrementDailyStat(ctx, g.s.GetDB(), target.ID, handlersbase.StatChallengeStarted); err != nil {
		entry.WithField("error", err.Error()).Warn("failed to increment started challenge stat")
	}

	commLang := g.s.GetLanguage(ctx, languageChatID, user)
	buttons, correctVariant := g.createCaptchaButtons(user.ID, challenge.SuccessUUID, commLang, captchaOptionsCount)
	rows := captchaKeyboardRows(buttons)
	inlineRows := make([][]api.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		inlineRows = append(inlineRows, api.NewInlineKeyboardRow(row...))
	}
	markup := api.NewInlineKeyboardMarkup(inlineRows...)

	var keys []string
	if isPublic {
		keys = challengeKeys
	} else {
		keys = privateChallengeKeys
	}
	randomKey := keys[tool.RandInt(0, len(keys)-1)]
	nameString := fmt.Sprintf("[%s](tg://user?id=%d)", api.EscapeText(api.ModeMarkdown, bot.GetFullName(user)), user.ID)

	args := []any{nameString}
	if !isPublic {
		args = append(args, g.chatLinkTitled(target))
	}
	args = append(args, correctVariant[1])
	msgText := strings.TrimSpace(fmt.Sprintf(i18n.Get(randomKey, commLang), args...))
	parseMode := api.ModeMarkdown
	if isPublic && settings.GatekeeperGreetingEnabled {
		greetingText := g.renderGreetingText(settings, user, target)
		if greetingText != "" && g.greetingParseMode(settings) == api.ModeMarkdownV2 {
			parseMode = api.ModeMarkdownV2
			msgText = composeGatekeeperMessage(greetingText, g.renderChallengeTextMarkdownV2(randomKey, commLang, user, correctVariant[1]))
		} else {
			msgText = composeGatekeeperMessage(greetingText, msgText)
		}
	}
	if msgText == "" {
		if err := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); err != nil {
			entry.WithField("error", err.Error()).Error("failed to delete empty challenge")
		}
		return nil
	}

	msg := api.NewMessage(recipientChatID, msgText)
	msg.ParseMode = parseMode
	msg.DisableNotification = isPublic
	msg.ReplyMarkup = &markup

	sentMsg, err := b.Send(msg)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to send gatekeeper challenge")
		if deleteErr := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); deleteErr != nil {
			entry.WithField("error", deleteErr.Error()).Error("failed to delete unsent challenge")
		}
		return errors.WithMessage(err, "cant send gatekeeper message")
	}

	challenge.ChallengeMessageID = sentMsg.MessageID
	if err := g.store.UpdateChallenge(ctx, challenge); err != nil {
		entry.WithField("error", err.Error()).Error("failed to update challenge message id")
	}

	return nil
}

func composeGatekeeperMessage(greetingText, challengeText string) string {
	greetingText = strings.TrimSpace(greetingText)
	challengeText = strings.TrimSpace(challengeText)

	switch {
	case greetingText != "" && challengeText != "":
		return greetingText + "\n\n" + challengeText
	case challengeText != "":
		return challengeText
	default:
		return greetingText
	}
}

func (g *Gatekeeper) sendGreeting(ctx context.Context, recipientChatID int64, target *api.Chat, user *api.User, settings *db.Settings, disableNotification bool) error {
	if target == nil || user == nil || settings == nil || !settings.GatekeeperGreetingEnabled {
		return nil
	}

	msgText := strings.TrimSpace(g.renderGreetingText(settings, user, target))
	if msgText == "" {
		return nil
	}

	msg := api.NewMessage(recipientChatID, msgText)
	msg.ParseMode = g.greetingParseMode(settings)
	msg.DisableNotification = disableNotification
	_, err := g.s.GetBot().Send(msg)
	if err != nil {
		return errors.WithMessage(err, "cant send greeting")
	}
	return nil
}

func (g *Gatekeeper) renderGreetingText(settings *db.Settings, user *api.User, target *api.Chat) string {
	if settings == nil || user == nil || target == nil {
		return ""
	}
	template := strings.TrimSpace(settings.GatekeeperGreetingText)
	if template == "" {
		return ""
	}
	if db.IsGatekeeperGreetingMarkdownV2Template(template) {
		template = db.StripGatekeeperGreetingTemplateSyntax(template)
		userPlaceholder := markdownV2TextMention(bot.GetFullName(user), user.ID)
		titlePlaceholder := api.EscapeText(api.ModeMarkdownV2, target.Title)
		linkPlaceholder := g.chatLinkTitledMarkdownV2(target)
		timeoutPlaceholder := api.EscapeText(api.ModeMarkdownV2, humanizeDurationShort(settings.GetChallengeTimeout()))

		template = strings.ReplaceAll(template, greetingPlaceholderUser, userPlaceholder)
		template = strings.ReplaceAll(template, greetingPlaceholderChatTitle, titlePlaceholder)
		template = strings.ReplaceAll(template, greetingPlaceholderChatLinkTitled, linkPlaceholder)
		template = strings.ReplaceAll(template, greetingPlaceholderTimeout, timeoutPlaceholder)

		return template
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

func (g *Gatekeeper) chatLinkTitledMarkdownV2(target *api.Chat) string {
	if target == nil {
		return ""
	}
	escapedTitle := api.EscapeText(api.ModeMarkdownV2, target.Title)
	if target.UserName == "" {
		return escapedTitle
	}
	return fmt.Sprintf("[%s](%s)", escapedTitle, api.EscapeText(api.ModeMarkdownV2, "https://t.me/"+target.UserName))
}

func (g *Gatekeeper) greetingParseMode(settings *db.Settings) string {
	if settings != nil && db.IsGatekeeperGreetingMarkdownV2Template(strings.TrimSpace(settings.GatekeeperGreetingText)) {
		return api.ModeMarkdownV2
	}
	return api.ModeMarkdown
}

func (g *Gatekeeper) renderChallengeTextMarkdownV2(templateKey, language string, user *api.User, variant string) string {
	if user == nil {
		return ""
	}

	template := escapeMarkdownV2FormatTemplate(i18n.Get(templateKey, language))
	nameString := markdownV2TextMention(bot.GetFullName(user), user.ID)
	variantString := api.EscapeText(api.ModeMarkdownV2, variant)

	return strings.TrimSpace(fmt.Sprintf(template, nameString, variantString))
}

func markdownV2TextMention(text string, userID int64) string {
	return fmt.Sprintf("[%s](%s)",
		api.EscapeText(api.ModeMarkdownV2, text),
		api.EscapeText(api.ModeMarkdownV2, fmt.Sprintf("tg://user?id=%d", userID)),
	)
}

func escapeMarkdownV2FormatTemplate(template string) string {
	parts := strings.Split(template, "%s")
	for i, part := range parts {
		parts[i] = api.EscapeText(api.ModeMarkdownV2, part)
	}
	return strings.Join(parts, "%s")
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
