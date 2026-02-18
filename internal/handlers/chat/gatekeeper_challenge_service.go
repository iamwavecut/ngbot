package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

func (g *Gatekeeper) handleChallenge(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (err error) {
	entry := g.getLogEntry().WithField("method", "handleChallenge")
	entry.Debug("handling challenge")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	b := g.s.GetBot()
	cq := u.CallbackQuery

	entry.WithFields(log.Fields{
		"data": cq.Data,
		"user": bot.GetUN(user),
		"chat": chat.ID,
	}).Debug("callback query data")

	joinerID, challengeUUID, err := func(s string) (int64, string, error) {
		entry := g.getLogEntry().WithField("method", "handleChallenge.parseCallbackData")
		entry.WithField("data", s).Debug("parsing callback data")
		parts := strings.Split(s, ";")
		if len(parts) != 2 {
			errInvalidString := errors.New("invalid string to split")
			entry.WithField("error", errInvalidString.Error()).Error("callback query data is invalid")
			return 0, "", errInvalidString
		}
		ID, err := strconv.ParseInt(parts[0], 10, 0)
		if err != nil {
			errCantParseUserID := errors.New("cant parse user ID")
			entry.WithField("error", errCantParseUserID.Error()).Error("callback query data is invalid")
			return 0, "", errCantParseUserID
		}
		entry.WithFields(log.Fields{"joinerID": ID, "challengeUUID": parts[1]}).Debug("parsed callback data")
		return ID, parts[1], nil
	}(cq.Data)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to parse callback query data")
		return err
	}

	if user.ID != joinerID {
		language := g.s.GetLanguage(ctx, chat.ID, user)
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", language))); err != nil {
			entry.WithField("error", err.Error()).Error("cant answer callback query")
		}
		return nil
	}

	challenge, err := g.store.GetChallenge(ctx, chat.ID, joinerID)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to fetch challenge")
		return err
	}
	if challenge == nil {
		entry.Debug("no user matched for challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("This challenge isn't your concern", g.s.GetLanguage(ctx, chat.ID, user)))); err != nil {
			entry.WithField("error", err.Error()).Error("cant answer callback query")
		}
		return nil
	}

	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: challenge.ChatID,
		},
	})
	if err != nil {
		entry.WithField("error", err.Error()).Error("cant get target chat info")
		return errors.WithMessage(err, "cant get target chat info")
	}

	language := g.s.GetLanguage(ctx, targetChat.ID, user)
	rejectDuration, rejectText, err := g.rejectConfig(ctx, targetChat.ID, language, targetChat.Title)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to build reject config")
		return err
	}

	if time.Now().After(challenge.ExpiresAt) {
		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, rejectText)); err != nil {
			entry.WithField("error", err.Error()).Error("cant answer callback query")
		}
		return g.failChallenge(ctx, challenge, rejectText, rejectDuration)
	}

	challenge.Attempts++
	if err := g.store.UpdateChallenge(ctx, challenge); err != nil {
		entry.WithField("error", err.Error()).Error("failed to update challenge")
		return err
	}
	if challenge.Attempts > maxChallengeAttempts {
		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, rejectText)); err != nil {
			entry.WithField("error", err.Error()).Error("cant answer callback query")
		}
		return g.failChallenge(ctx, challenge, rejectText, rejectDuration)
	}

	if challenge.SuccessUUID != challengeUUID {
		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, rejectText)); err != nil {
			entry.WithField("error", err.Error()).Error("cant answer callback query")
		}
		return g.failChallenge(ctx, challenge, rejectText, rejectDuration)
	}

	if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", language))); err != nil {
		entry.WithField("error", err.Error()).Error("cant answer callback query")
	}

	return g.completeChallenge(ctx, challenge, &targetChat, language)
}

func (g *Gatekeeper) completeChallenge(ctx context.Context, challenge *db.Challenge, target *api.ChatFullInfo, language string) error {
	entry := g.getLogEntry().WithField("method", "completeChallenge")
	b := g.s.GetBot()

	if challenge.ChallengeMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, b, challenge.CommChatID, challenge.ChallengeMessageID); err != nil {
			entry.WithField("error", err.Error()).Error("cant delete challenge message")
		}
	}

	if challenge.CommChatID != challenge.ChatID {
		if err := bot.ApproveJoinRequest(ctx, b, challenge.UserID, challenge.ChatID); err != nil {
			entry.WithField("error", err.Error()).Error("cant approve join request")
		}
		msg := api.NewMessage(
			challenge.CommChatID,
			fmt.Sprintf(
				i18n.Get("Awesome, you're good to go! Feel free to start chatting in the group \"%s\".", language),
				api.EscapeText(api.ModeMarkdown, target.Title),
			),
		)
		msg.ParseMode = api.ModeMarkdown
		_ = tool.Err(b.Send(msg))
	} else {
		_ = bot.UnrestrictChatting(ctx, b, challenge.UserID, challenge.ChatID)
	}

	return g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID)
}

func (g *Gatekeeper) failChallenge(ctx context.Context, challenge *db.Challenge, rejectText string, rejectDuration time.Duration) error {
	entry := g.getLogEntry().WithField("method", "failChallenge")
	b := g.s.GetBot()

	if challenge.ChallengeMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, b, challenge.CommChatID, challenge.ChallengeMessageID); err != nil {
			entry.WithField("error", err.Error()).Error("cant delete challenge message")
		}
	}

	if challenge.JoinMessageID != 0 {
		if err := bot.DeleteChatMessage(ctx, b, challenge.ChatID, challenge.JoinMessageID); err != nil {
			entry.WithField("error", err.Error()).Error("cant delete join message")
		}
	}

	if err := bot.BanUserFromChat(ctx, b, challenge.UserID, challenge.ChatID, time.Now().Add(rejectDuration).Unix()); err != nil {
		entry.WithField("error", err.Error()).Error("cant ban user")
	}

	if challenge.CommChatID != challenge.ChatID {
		if err := bot.DeclineJoinRequest(ctx, b, challenge.UserID, challenge.ChatID); err != nil {
			entry.WithField("error", err.Error()).Error("decline join request failed")
		}
		msg := api.NewMessage(challenge.CommChatID, rejectText)
		sentMsg, err := b.Send(msg)
		if err == nil && sentMsg.MessageID != 0 {
			time.AfterFunc(rejectDuration, func() {
				_ = bot.DeleteChatMessage(ctx, b, challenge.CommChatID, sentMsg.MessageID)
			})
		}
	}

	return g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID)
}

func (g *Gatekeeper) rejectConfig(ctx context.Context, chatID int64, language string, title string) (time.Duration, string, error) {
	settings, err := g.fetchAndValidateSettings(ctx, chatID)
	if err != nil {
		return 0, "", err
	}
	rejectDuration := settings.GetRejectTimeout()
	rejectMinutes := int(rejectDuration.Minutes())
	if rejectMinutes < 1 {
		rejectMinutes = 1
	}
	rejectText := fmt.Sprintf(
		i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", language),
		title,
		strconv.Itoa(rejectMinutes),
	)
	return rejectDuration, rejectText, nil
}
