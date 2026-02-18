package handlers

import (
	"context"
	"fmt"
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

func (g *Gatekeeper) handleNewChatMembersV2(ctx context.Context, u *api.Update, chat *api.Chat) error {
	entry := g.getLogEntry()

	if chat == nil {
		entry.Debug("chat is nil")
		return nil
	}

	if u == nil {
		entry.Debug("update is nil")
		return nil
	}

	select {
	case <-ctx.Done():
		entry.Debug("Context cancelled")
		return ctx.Err()
	default:
	}

	if u.Message != nil && u.Message.NewChatMembers != nil {
		entry.Info("Adding new chat members")
		for _, member := range u.Message.NewChatMembers {
			if g.banChecker.IsKnownBanned(member.ID) {
				entry.WithField("userID", member.ID).WithField("name", bot.GetUN(&member)).Info("recent joiner is known banned")
				err := g.banChecker.BanUserWithMessage(ctx, chat.ID, member.ID, u.Message.MessageID)
				if err != nil {
					entry.WithField("error", err.Error()).Error("failed to ban known banned user")
				}
				continue
			}
			userName := bot.GetUN(&member)
			entry := entry.WithField("user", userName)
			entry.Debug("Saving user as recent joiner")

			// Create RecentJoiner record
			recentJoiner := &db.RecentJoiner{
				UserID:        member.ID,
				ChatID:        chat.ID,
				JoinedAt:      time.Now(),
				JoinMessageID: u.Message.MessageID,
				Username:      userName,
				Processed:     false,
				IsSpammer:     false,
			}

			_, err := g.store.AddChatRecentJoiner(ctx, recentJoiner)
			if err != nil {
				entry.WithField("error", err.Error()).Error("Failed to save recent joiner")
			}

			entry.Info("Saved user as recent joiner")
		}
	}

	return nil
}

func (g *Gatekeeper) handleChatJoinRequest(ctx context.Context, u *api.Update) error {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "handleChatJoinRequest",
		"chat":   u.ChatJoinRequest.Chat.Title,
	})
	entry.Info("Handling chat join request")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	target := &u.ChatJoinRequest.Chat

	entry.Info("Getting bot chat info")
	comm, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	})
	if err != nil {
		entry.WithField("error", err.Error()).Error("Failed to get bot chat info")
		return err
	}

	return g.handleJoin(ctx, u, []api.User{u.ChatJoinRequest.From}, target, &comm)
}

func (g *Gatekeeper) handleJoin(ctx context.Context, u *api.Update, jus []api.User, target *api.Chat, comm *api.ChatFullInfo) (err error) {
	entry := g.getLogEntry().WithField("method", "handleJoin")
	entry.Debug("Handling join")

	if target == nil || comm == nil {
		entry.Error("Target or comm is nil")
		return errors.New("target or comm is nil")
	}
	b := g.s.GetBot()
	settings, err := g.fetchAndValidateSettings(ctx, target.ID)
	if err != nil {
		return err
	}
	challengeTimeout := settings.GetChallengeTimeout()

	for _, ju := range jus {
		if ju.IsBot {
			entry.WithField("user", bot.GetUN(&ju)).Debug("Skipping bot user")
			continue
		}
		isPublic := comm.Chat.ID == target.ID
		if isPublic {
			entry.WithFields(log.Fields{
				"user":   bot.GetUN(&ju),
				"chatID": target.ID,
			}).Info("Restricting user in chat")
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
				entry.WithField("error", err.Error()).Error("Failed to restrict user")
			}
		}

		commLang := g.s.GetLanguage(ctx, comm.Chat.ID, &ju)
		now := time.Now()
		challenge := &db.Challenge{
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
			entry.WithField("error", err.Error()).Error("Failed to create challenge")
			return err
		}

		entry.Debug("Creating captcha buttons")
		buttons, correctVariant := g.createCaptchaButtons(ju.ID, challenge.SuccessUUID, commLang)

		var keys []string
		if isPublic {
			keys = challengeKeys
		} else {
			keys = privateChallengeKeys
		}

		randomKey := keys[tool.RandInt(0, len(keys)-1)]
		nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, bot.GetFullName(&ju)), ju.ID)

		args := []interface{}{nameString}
		if !isPublic {
			args = append(args, api.EscapeText(api.ModeMarkdown, target.Title))
		}
		args = append(args, correctVariant[1])
		msgText := fmt.Sprintf(i18n.Get(randomKey, commLang), args...)
		msg := api.NewMessage(comm.Chat.ID, msgText)
		msg.ParseMode = api.ModeMarkdown
		if isPublic {
			msg.DisableNotification = true
		}

		kb := api.NewInlineKeyboardMarkup(
			api.NewInlineKeyboardRow(buttons...),
		)
		msg.ReplyMarkup = kb
		entry.Debug("Sending challenge message")
		sentMsg, err := b.Send(msg)
		if err != nil {
			entry.WithField("error", err.Error()).Error("Failed to send challenge message")
			_ = g.store.DeleteChallenge(ctx, comm.Chat.ID, ju.ID)
			return errors.WithMessage(err, "cant send")
		}
		challenge.ChallengeMessageID = sentMsg.MessageID
		if err := g.store.UpdateChallenge(ctx, challenge); err != nil {
			entry.WithField("error", err.Error()).Error("Failed to update challenge")
		}
	}
	entry.Debug("Exiting handleJoin method")
	return nil
}
