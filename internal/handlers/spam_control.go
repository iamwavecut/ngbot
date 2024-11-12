package handlers

import (
	"context"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
	log "github.com/sirupsen/logrus"
)

type SpamControl struct {
	s          bot.Service
	config     config.SpamControl
	banService BanService
}

func NewSpamControl(s bot.Service, config config.SpamControl, banService BanService) *SpamControl {
	return &SpamControl{
		s:          s,
		config:     config,
		banService: banService,
	}
}

func (sc *SpamControl) ProcessSuspectMessage(ctx context.Context, msg *api.Message, lang string) error {
	spamCase, err := sc.s.GetDB().GetActiveSpamCase(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		return fmt.Errorf("failed to get active spam case: %w", err)
	}

	if spamCase == nil {
		spamCase, err = sc.s.GetDB().CreateSpamCase(ctx, &db.SpamCase{
			ChatID:      msg.Chat.ID,
			UserID:      msg.From.ID,
			MessageText: msg.Text,
			CreatedAt:   time.Now(),
			Status:      "pending",
		})
		if err != nil {
			return fmt.Errorf("failed to create spam case: %w", err)
		}
	}

	notifMsg := sc.createInChatVoting(msg, spamCase.ID, lang)
	notification, err := sc.s.GetBot().Send(notifMsg)
	if err != nil {
		log.WithError(err).Error("failed to send notification")
	} else {
		spamCase.NotificationMessageID = notification.MessageID

		time.AfterFunc(sc.config.SuspectNotificationTimeout, func() {
			if _, err := sc.s.GetBot().Request(api.NewDeleteMessage(msg.Chat.ID, notification.MessageID)); err != nil {
				log.WithError(err).Error("failed to delete notification")
			}
		})
	}

	if err := sc.s.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		log.WithError(err).Error("failed to update spam case")
	}

	time.AfterFunc(time.Duration(sc.config.VotingTimeoutMinutes)*time.Minute, func() {
		if err := sc.resolveCase(context.Background(), spamCase.ID); err != nil {
			log.WithError(err).Error("failed to resolve spam case")
		}
	})
	return nil
}

func (sc *SpamControl) ProcessSpamMessage(ctx context.Context, msg *api.Message, isSpam bool, lang string) error {
	spamCase, err := sc.s.GetDB().GetActiveSpamCase(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		return fmt.Errorf("failed to get active spam case: %w", err)
	}

	if spamCase == nil {
		spamCase, err = sc.s.GetDB().CreateSpamCase(ctx, &db.SpamCase{
			ChatID:      msg.Chat.ID,
			UserID:      msg.From.ID,
			MessageText: msg.Text,
			CreatedAt:   time.Now(),
			Status:      "pending",
		})
		if err != nil {
			return fmt.Errorf("failed to create spam case: %w", err)
		}
	}

	if err := bot.DeleteChatMessage(ctx, sc.s.GetBot(), msg.Chat.ID, msg.MessageID); err != nil {
		log.WithError(err).Error("failed to delete message")
	}

	var notifMsg api.Chattable
	if sc.config.LogChannelID != 0 {
		channelMsg := sc.createChannelPost(msg, spamCase.ID, lang)
		sent, err := sc.s.GetBot().Send(channelMsg)
		if err != nil {
			return fmt.Errorf("failed to send channel post: %w", err)
		}
		spamCase.ChannelPostID = sent.MessageID

		channelPostLink := ""
		if sent.From != nil {
			if sent.From.UserName != "" {
				channelPostLink = fmt.Sprintf("https://t.me/%s/%d", sent.From.UserName, sent.MessageID)
			} else {
				channelPostLink = fmt.Sprintf("https://t.me/c/%d/%d", sent.Chat.ID, sent.MessageID)
			}
		}
		notifMsg = sc.createChannelNotification(msg, channelPostLink, lang)
	} else {
		notifMsg = sc.createInChatVoting(msg, spamCase.ID, lang)
	}
	notification, err := sc.s.GetBot().Send(notifMsg)
	if err != nil {
		log.WithError(err).Error("failed to send notification")
	} else {
		spamCase.NotificationMessageID = notification.MessageID

		time.AfterFunc(sc.config.SuspectNotificationTimeout, func() {
			if _, err := sc.s.GetBot().Request(api.NewDeleteMessage(msg.Chat.ID, notification.MessageID)); err != nil {
				log.WithError(err).Error("failed to delete notification")
			}
		})
	}

	if err := sc.s.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		log.WithError(err).Error("failed to update spam case")
	}

	time.AfterFunc(time.Duration(sc.config.VotingTimeoutMinutes)*time.Minute, func() {
		if err := sc.resolveCase(context.Background(), spamCase.ID); err != nil {
			log.WithError(err).Error("failed to resolve spam case")
		}
	})

	return nil
}

func (sc *SpamControl) createInChatVoting(msg *api.Message, caseID int64, lang string) api.Chattable {
	text := fmt.Sprintf(i18n.Get("⚠️ Potential spam message from %s\n\nMessage: %s\n\nPlease vote:", lang),
		bot.GetUN(msg.From),
		msg.Text,
	)

	markup := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonData(i18n.Get("✅ Not Spam", lang), fmt.Sprintf("spam_vote:%d:0", caseID)),
			api.NewInlineKeyboardButtonData(i18n.Get("🚫 Spam", lang), fmt.Sprintf("spam_vote:%d:1", caseID)),
		),
	)

	replyMsg := api.NewMessage(msg.Chat.ID, text)
	replyMsg.ParseMode = api.ModeMarkdown
	replyMsg.DisableNotification = true
	replyMsg.LinkPreviewOptions.IsDisabled = true
	replyMsg.ReplyMarkup = &markup
	return replyMsg
}

func (sc *SpamControl) createChannelPost(msg *api.Message, caseID int64, lang string) api.Chattable {
	text := fmt.Sprintf(i18n.Get("Potential spam message:\n\nFrom: %s\nChat: %s\nMessage: %s", lang),
		bot.GetUN(msg.From),
		msg.Chat.Title,
		msg.Text,
	)

	markup := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonData(i18n.Get("Not Spam", lang), fmt.Sprintf("spam_vote:%d:0", caseID)),
			api.NewInlineKeyboardButtonData(i18n.Get("Spam", lang), fmt.Sprintf("spam_vote:%d:1", caseID)),
		),
	)

	replyMsg := api.NewMessage(sc.config.LogChannelID, text)
	replyMsg.ReplyMarkup = &markup
	return replyMsg
}

func (sc *SpamControl) createChannelNotification(msg *api.Message, channelPostLink string, lang string) api.Chattable {
	from := bot.GetUN(msg.From)
	text := fmt.Sprintf(i18n.Get("Message from %s is being reviewed for spam\n\nAppeal here: [link](%s)", lang), from, channelPostLink)
	notificationMsg := api.NewMessage(msg.Chat.ID, text)
	notificationMsg.ParseMode = api.ModeMarkdown
	notificationMsg.DisableNotification = true
	notificationMsg.LinkPreviewOptions.IsDisabled = true

	return notificationMsg
}

func (sc *SpamControl) resolveCase(ctx context.Context, caseID int64) error {
	entry := sc.getLogEntry().WithField("method", "resolveCase").WithField("case_id", caseID)
	spamCase, err := sc.s.GetDB().GetSpamCase(ctx, caseID)
	if err != nil {
		return fmt.Errorf("failed to get case: %w", err)
	}
	if spamCase.Status != "pending" {
		entry.WithField("status", spamCase.Status).Debug("case is not pending, skipping")
		return nil
	}

	votes, err := sc.s.GetDB().GetSpamVotes(ctx, caseID)
	if err != nil {
		return fmt.Errorf("failed to get votes: %w", err)
	}

	members, err := sc.s.GetDB().GetMembers(ctx, spamCase.ChatID)
	if err != nil {
		log.WithError(err).Error("failed to get members count")
	}

	minVotersFromPercentage := int(float64(len(members)) * sc.config.MinVotersPercentage / 100)

	requiredVoters := max(sc.config.MinVoters, minVotersFromPercentage)

	if len(votes) >= requiredVoters {
		yesVotes := 0
		noVotes := 0
		for _, vote := range votes {
			if vote.Vote {
				yesVotes++
			} else {
				noVotes++
			}
		}

		if noVotes >= yesVotes {
			spamCase.Status = "spam"

		} else {
			spamCase.Status = "false_positive"
		}
	} else {
		entry.WithField("required_voters", requiredVoters).WithField("got_votes", len(votes)).Debug("not enough voters")
		spamCase.Status = "spam"
	}

	if spamCase.Status == "spam" {
		if err := sc.banService.BanUser(ctx, spamCase.ChatID, spamCase.UserID, 0); err != nil {
			log.WithError(err).Error("failed to ban user")
		}
	} else {
		if err := sc.banService.UnmuteUser(ctx, spamCase.ChatID, spamCase.UserID); err != nil {
			log.WithError(err).Error("failed to unmute user")
		}
	}

	now := time.Now()
	spamCase.ResolvedAt = &now
	if err := sc.s.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		return fmt.Errorf("failed to update case: %w", err)
	}
	return nil
}

func (sc *SpamControl) getLogEntry() *log.Entry {
	return log.WithField("object", "SpamControl")
}
