package handlers

import (
	"context"
	stderrors "errors"
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
	entry := g.getLogEntry().WithField(logFieldMethod, "handleNewChatMembersV2")

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
	subfeaturesEnabled := settings.GatekeeperCaptchaEnabled || settings.GatekeeperGreetingEnabled

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	for _, member := range u.Message.NewChatMembers {
		joiner, err := g.recordRecentJoiner(ctx, chat.ID, &member, u.Message.MessageID)
		if err != nil {
			entry.WithField(logFieldError, err.Error()).Error("failed to save recent joiner")
		}
		isNotSpammer, err := g.store.IsChatNotSpammer(ctx, chat.ID, member.ID, member.UserName)
		if err != nil {
			entry.WithField(logFieldError, err.Error()).Error("failed to check manual not-spammer override")
			continue
		}
		if isNotSpammer {
			continue
		}
		banned, err := g.banChecker.CheckBan(ctx, member.ID)
		if err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to check ban for new chat member")
			continue
		}
		if banned {
			g.processKnownBannedJoinedUser(ctx, chat.ID, member.ID, joinerMessageID(joiner, u.Message.MessageID))
			if err := g.store.ProcessRecentJoiner(ctx, chat.ID, member.ID, true); err != nil {
				entry.WithField(logFieldError, err.Error()).Error("failed to process banned recent joiner")
			}
			continue
		}
		if member.IsBot {
			continue
		}
		if !subfeaturesEnabled {
			continue
		}
		if err := g.backfillPublicChallengeJoinMessageID(ctx, chat.ID, member.ID, u.Message.MessageID); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to backfill join message id for public challenge")
		}
	}

	return nil
}

func (g *Gatekeeper) handleChatMember(ctx context.Context, u *api.Update, settings *db.Settings) {
	entry := g.getLogEntry().WithField(logFieldMethod, "handleChatMember")

	if u == nil || u.ChatMember == nil {
		entry.Debug("chat member update is nil")
		return
	}
	if settings == nil {
		entry.Debug("settings are nil")
		return
	}
	subfeaturesEnabled := settings.GatekeeperCaptchaEnabled || settings.GatekeeperGreetingEnabled
	if !isChatMemberJoinTransition(u.ChatMember) {
		entry.Debug("chat member update is not a new join transition")
		return
	}

	member := u.ChatMember.NewChatMember.User
	if member == nil {
		entry.Debug("chat member user is nil")
		return
	}

	chat := &u.ChatMember.Chat
	joiner, err := g.recordRecentJoiner(ctx, chat.ID, member, 0)
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: member.ID,
			logFieldError:  err.Error(),
		}).Error("failed to save recent joiner")
	}

	isNotSpammer, err := g.store.IsChatNotSpammer(ctx, chat.ID, member.ID, member.UserName)
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: member.ID,
			logFieldError:  err.Error(),
		}).Error("failed to check manual not-spammer override")
		return
	}
	if isNotSpammer {
		return
	}

	banned, err := g.banChecker.CheckBan(ctx, member.ID)
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: member.ID,
			logFieldError:  err.Error(),
		}).Error("failed to check ban for chat member")
		return
	}
	if banned {
		g.processKnownBannedJoinedUser(ctx, chat.ID, member.ID, joinerMessageID(joiner, 0))
		if err := g.store.ProcessRecentJoiner(ctx, chat.ID, member.ID, true); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to process banned recent joiner")
		}
		return
	}

	if member.IsBot {
		return
	}

	if !subfeaturesEnabled {
		entry.Debug("gatekeeper subfeatures are disabled")
		return
	}

	handoffChallenge, err := g.store.GetPassedJoinRequestChallengeByChatUser(ctx, chat.ID, member.ID)
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: member.ID,
			logFieldError:  err.Error(),
		}).Error("failed to load approved join request handoff challenge")
	}
	if handoffChallenge != nil {
		if err := g.sendGreeting(ctx, chat.ID, chat, member, settings, true); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to send gatekeeper greeting after approved join request")
		}
		if _, err := g.store.DeleteChallengeInstance(ctx, handoffChallenge.ChallengeID, db.ChallengeStatusPassedWaitingMemberJoin); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to delete approved join request handoff challenge")
		}
		return
	}

	if u.ChatMember.ViaJoinRequest {
		if err := g.sendGreeting(ctx, chat.ID, chat, member, settings, true); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to send gatekeeper greeting for approved join request")
		}
		return
	}

	switch {
	case settings.GatekeeperCaptchaEnabled:
		if err := g.startChallenge(ctx, u, member, chat, chat.ID, chat.ID, settings); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to handle gatekeeper captcha for new member")
		}
	case settings.GatekeeperGreetingEnabled:
		if err := g.sendGreeting(ctx, chat.ID, chat, member, settings, true); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: member.ID,
				logFieldError:  err.Error(),
			}).Error("failed to send gatekeeper greeting for new member")
		}
	}
}

func (g *Gatekeeper) handleChatJoinRequest(ctx context.Context, u *api.Update, settings *db.Settings) error {
	entry := g.getLogEntry().WithField(logFieldMethod, "handleChatJoinRequest")

	if u == nil || u.ChatJoinRequest == nil {
		entry.Debug("chat join request is nil")
		return nil
	}
	if settings == nil {
		entry.Debug("settings are nil")
		return nil
	}
	isNotSpammer, err := g.store.IsChatNotSpammer(ctx, u.ChatJoinRequest.Chat.ID, u.ChatJoinRequest.From.ID, u.ChatJoinRequest.From.UserName)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to check manual not-spammer override")
		return nil
	}
	if !isNotSpammer {
		banned, err := g.banChecker.CheckBan(ctx, u.ChatJoinRequest.From.ID)
		if err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: u.ChatJoinRequest.From.ID,
				logFieldError:  err.Error(),
			}).Error("failed to check ban for chat join request")
			return err
		}
		if banned {
			g.processKnownBannedJoinRequest(ctx, u.ChatJoinRequest)
			return nil
		}
	}
	if !settings.GatekeeperCaptchaEnabled && !settings.GatekeeperGreetingEnabled {
		if u.ChatJoinRequest.QueryID != "" {
			if err := bot.AnswerJoinRequestQuery(ctx, g.bot, u.ChatJoinRequest.QueryID, bot.JoinRequestQueryResultQueue); err != nil {
				entry.WithField(logFieldError, err.Error()).Error("failed to queue join request query with disabled gatekeeper subfeatures")
			}
		}
		entry.Debug("both gatekeeper subfeatures are disabled")
		return nil
	}
	if !settings.GatekeeperCaptchaEnabled {
		if u.ChatJoinRequest.QueryID != "" {
			if err := bot.AnswerJoinRequestQuery(ctx, g.bot, u.ChatJoinRequest.QueryID, bot.JoinRequestQueryResultQueue); err != nil {
				entry.WithField(logFieldError, err.Error()).Error("failed to queue join request query with disabled captcha")
			}
		}
		entry.Debug("captcha is disabled for join requests, leaving request for manual review")
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if u.ChatJoinRequest.QueryID != "" && g.joinCaptchaPublicURL() != "" {
		return g.startJoinRequestWebAppChallenge(ctx, u.ChatJoinRequest, settings)
	}
	if u.ChatJoinRequest.QueryID != "" {
		if err := bot.AnswerJoinRequestQuery(ctx, g.bot, u.ChatJoinRequest.QueryID, bot.JoinRequestQueryResultQueue); err != nil {
			entry.WithField(logFieldError, err.Error()).Error("failed to queue join request query before DM fallback")
		}
	}

	if _, err := bot.GetChat(ctx, g.bot, api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	}); err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to get user private chat info")
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
	entry := g.getLogEntry().WithField(logFieldMethod, "startChallenge")

	if user == nil || target == nil {
		return errors.New("user or target chat is nil")
	}
	if settings == nil {
		return errors.New("settings are nil")
	}
	if !settings.GatekeeperCaptchaEnabled || user.IsBot {
		return nil
	}

	b := g.bot
	challengeTimeout := settings.GetChallengeTimeout()
	isPublic := recipientChatID == target.ID
	restricted := false

	if isPublic {
		if _, err := b.RequestWithContext(ctx, api.RestrictChatMemberConfig{
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
			entry.WithField(logFieldError, err.Error()).Error("failed to restrict user")
			return errors.WithMessage(err, "restrict user before challenge")
		}
		restricted = true
	}

	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:   recipientChatID,
		UserID:       user.ID,
		ChatID:       target.ID,
		Status:       db.ChallengeStatusPending,
		SuccessUUID:  uuid.New(),
		UserLanguage: strings.TrimSpace(user.LanguageCode),
		CreatedAt:    now,
		ExpiresAt:    now.Add(challengeTimeout),
	}
	if u != nil && u.Message != nil {
		challenge.JoinMessageID = u.Message.MessageID
	}
	if u != nil && u.ChatJoinRequest != nil {
		challenge.JoinRequestQueryID = u.ChatJoinRequest.QueryID
	}
	if _, err := g.store.CreateChallenge(ctx, challenge); err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to create challenge")
		if restricted {
			return stderrors.Join(err, bot.UnrestrictChatting(ctx, b, user.ID, target.ID))
		}
		return err
	}
	if err := handlersbase.IncrementDailyStat(ctx, g.stats, target.ID, handlersbase.StatChallengeStarted); err != nil {
		entry.WithField(logFieldError, err.Error()).Warn("failed to increment started challenge stat")
	}

	sentMessageID, err := g.sendChallengeMessage(ctx, challenge, user, target, languageChatID, settings)
	if err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to send gatekeeper challenge")
		return stderrors.Join(err, g.compensateChallengeActivation(ctx, challenge, restricted))
	}
	if sentMessageID == 0 {
		return g.compensateChallengeActivation(ctx, challenge, restricted)
	}

	attached, err := g.store.AttachChallengeMessage(ctx, challenge.ChallengeID, db.ChallengeStatusPending, sentMessageID)
	if err != nil || !attached {
		if err == nil {
			err = errors.New("challenge instance changed before message binding")
		}
		entry.WithField(logFieldError, err.Error()).Error("failed to attach challenge message")
		_ = bot.DeleteChatMessage(ctx, b, recipientChatID, sentMessageID)
		return stderrors.Join(err, g.compensateChallengeActivation(ctx, challenge, restricted))
	}
	challenge.ChallengeMessageID = sentMessageID

	return nil
}

func (g *Gatekeeper) compensateChallengeActivation(ctx context.Context, challenge *db.Challenge, restricted bool) error {
	if challenge == nil {
		return nil
	}
	if !restricted {
		_, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, db.ChallengeStatusPending)
		return err
	}
	claimed, err := g.store.CompleteExternalAction(
		ctx,
		challenge.ChallengeID,
		db.ChallengeStatusPending,
		db.ChallengeStatusUnrestrictPending,
		time.Time{},
	)
	if err != nil {
		return err
	}
	if !claimed {
		return errors.New("challenge changed before activation compensation")
	}
	challenge.Status = db.ChallengeStatusUnrestrictPending
	return g.processChallengeActionWithoutStats(ctx, challenge)
}

func (g *Gatekeeper) sendChallengeMessage(
	ctx context.Context,
	challenge *db.Challenge,
	user *api.User,
	target *api.Chat,
	languageChatID int64,
	settings *db.Settings,
) (int, error) {
	isPublic := challenge.CommChatID == challenge.ChatID
	commLang := g.dmLanguage(user.LanguageCode, user)
	if isPublic {
		commLang = g.s.GetLanguage(ctx, languageChatID, user)
	}
	buttons, correctVariant := g.createCaptchaButtons(user.ID, challenge.SuccessUUID, commLang, normalizeCaptchaOptionsCount(settings.GatekeeperCaptchaOptionsCount))
	rows := captchaKeyboardRows(buttons)
	inlineRows := make([][]api.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		inlineRows = append(inlineRows, api.NewInlineKeyboardRow(row...))
	}
	markup := api.NewInlineKeyboardMarkup(inlineRows...)

	keys := privateChallengeKeys
	if isPublic {
		keys = challengeKeys
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
		return 0, nil
	}

	msg := api.NewMessage(challenge.CommChatID, msgText)
	msg.ParseMode = parseMode
	msg.DisableNotification = isPublic
	msg.ReplyMarkup = &markup
	sent, err := bot.Send(ctx, g.bot, msg)
	if err != nil {
		return 0, errors.WithMessage(err, "send gatekeeper message")
	}
	return sent.MessageID, nil
}

func (g *Gatekeeper) recordRecentJoiner(ctx context.Context, chatID int64, user *api.User, joinMessageID int) (*db.RecentJoiner, error) {
	if user == nil {
		return nil, nil
	}

	recentJoiner := &db.RecentJoiner{
		UserID:        user.ID,
		ChatID:        chatID,
		JoinedAt:      time.Now(),
		JoinMessageID: joinMessageID,
		Username:      user.UserName,
		Processed:     false,
		IsSpammer:     false,
	}
	return g.store.AddChatRecentJoiner(ctx, recentJoiner)
}

func (g *Gatekeeper) processKnownBannedJoinRequest(ctx context.Context, request *api.ChatJoinRequest) {
	if request == nil {
		return
	}

	entry := g.getLogEntry().WithField(logFieldMethod, "processKnownBannedJoinRequest")
	chatID := request.Chat.ID
	userID := request.From.ID

	var err error
	if request.QueryID != "" {
		err = bot.AnswerJoinRequestQuery(ctx, g.bot, request.QueryID, bot.JoinRequestQueryResultDecline)
	} else {
		err = bot.DeclineJoinRequest(ctx, g.bot, userID, chatID)
	}
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: userID,
			logFieldError:  err.Error(),
		}).Error("failed to decline banned join request")
	}
	if g.banChecker != nil {
		if err := g.banChecker.BanUserWithMessage(ctx, chatID, userID, 0); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: userID,
				logFieldError:  err.Error(),
			}).Error("failed to ban known banned join requester")
		}
	}

	g.cleanupKnownBannedArtifacts(ctx, chatID, userID, 0)
}

func (g *Gatekeeper) processKnownBannedJoinedUser(ctx context.Context, chatID, userID int64, joinMessageID int) {
	entry := g.getLogEntry().WithField(logFieldMethod, "processKnownBannedJoinedUser")

	if g.banChecker != nil {
		if err := g.banChecker.BanUserWithMessage(ctx, chatID, userID, joinMessageID); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: userID,
				logFieldError:  err.Error(),
			}).Error("failed to ban known banned joined user")
		}
	}

	g.cleanupKnownBannedArtifacts(ctx, chatID, userID, joinMessageID)
}

func (g *Gatekeeper) cleanupKnownBannedArtifacts(ctx context.Context, chatID, userID int64, joinMessageID int) {
	entry := g.getLogEntry().WithField(logFieldMethod, "cleanupKnownBannedArtifacts")
	deletedJoinMessages := make(map[int]struct{})
	cleanedChallenges := make(map[string]struct{})

	cleanupChallenge := func(challenge *db.Challenge) {
		if challenge == nil {
			return
		}
		key := fmt.Sprintf("%d:%d:%d", challenge.CommChatID, challenge.UserID, challenge.ChatID)
		if _, ok := cleanedChallenges[key]; ok {
			return
		}
		cleanedChallenges[key] = struct{}{}

		if challenge.ChallengeMessageID != 0 {
			if err := bot.DeleteChatMessage(ctx, g.bot, challenge.CommChatID, challenge.ChallengeMessageID); err != nil {
				entry.WithFields(log.Fields{
					logFieldUserID:    userID,
					logFieldMessageID: challenge.ChallengeMessageID,
					logFieldError:     err.Error(),
				}).Error("failed to delete known banned challenge message")
			}
		}
		if challenge.JoinMessageID != 0 {
			if err := bot.DeleteChatMessage(ctx, g.bot, challenge.ChatID, challenge.JoinMessageID); err != nil {
				entry.WithFields(log.Fields{
					logFieldUserID:    userID,
					logFieldMessageID: challenge.JoinMessageID,
					logFieldError:     err.Error(),
				}).Error("failed to delete known banned join message")
			}
			deletedJoinMessages[challenge.JoinMessageID] = struct{}{}
		}
		if _, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, challenge.Status); err != nil {
			entry.WithFields(log.Fields{
				logFieldUserID: userID,
				logFieldError:  err.Error(),
			}).Error("failed to delete known banned challenge row")
		}
	}

	handoffChallenge, err := g.store.GetPassedJoinRequestChallengeByChatUser(ctx, chatID, userID)
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: userID,
			logFieldError:  err.Error(),
		}).Error("failed to load known banned handoff challenge")
	}
	cleanupChallenge(handoffChallenge)

	challenge, err := g.store.GetChallengeByChatUser(ctx, chatID, userID)
	if err != nil {
		entry.WithFields(log.Fields{
			logFieldUserID: userID,
			logFieldError:  err.Error(),
		}).Error("failed to load known banned challenge")
	}
	cleanupChallenge(challenge)

	if joinMessageID != 0 {
		if _, ok := deletedJoinMessages[joinMessageID]; !ok {
			if err := bot.DeleteChatMessage(ctx, g.bot, chatID, joinMessageID); err != nil {
				entry.WithFields(log.Fields{
					logFieldUserID:    userID,
					logFieldMessageID: joinMessageID,
					logFieldError:     err.Error(),
				}).Error("failed to delete known banned join message")
			}
		}
	}
}

func joinerMessageID(joiner *db.RecentJoiner, fallback int) int {
	if joiner != nil && joiner.JoinMessageID != 0 {
		return joiner.JoinMessageID
	}
	return fallback
}

func (g *Gatekeeper) backfillPublicChallengeJoinMessageID(ctx context.Context, chatID, userID int64, joinMessageID int) error {
	if joinMessageID == 0 {
		return nil
	}

	challenge, err := g.store.GetChallengeByChatUser(ctx, chatID, userID)
	if err != nil {
		return err
	}
	if challenge == nil {
		return nil
	}
	if challenge.CommChatID != challenge.ChatID || challenge.Status != db.ChallengeStatusPending || challenge.JoinMessageID != 0 {
		return nil
	}

	_, err = g.store.AttachJoinMessage(ctx, challenge.ChallengeID, db.ChallengeStatusPending, joinMessageID)
	return err
}

func isChatMemberJoinTransition(update *api.ChatMemberUpdated) bool {
	if update == nil {
		return false
	}
	return !isCurrentChatMember(update.OldChatMember) && isCurrentChatMember(update.NewChatMember)
}

func isCurrentChatMember(member api.ChatMember) bool {
	switch member.Status {
	case "creator", "administrator", telegramMemberStatus:
		return true
	case "restricted":
		return member.IsMember
	default:
		return false
	}
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
	_, err := bot.Send(ctx, g.bot, msg)
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
	return fmt.Sprintf(
		"[%s](%s)",
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
