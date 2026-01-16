package handlers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
	verbose    bool
}

func NewSpamControl(s bot.Service, config config.SpamControl, banService BanService, verbose bool) *SpamControl {
	return &SpamControl{
		s:          s,
		config:     config,
		banService: banService,
		verbose:    verbose,
	}
}

func (sc *SpamControl) ProcessSuspectMessage(ctx context.Context, msg *api.Message, lang string) error {
	if msg == nil {
		return nil
	}
	_, err := sc.preprocessMessage(ctx, msg, &msg.Chat, lang, true)
	return err
}

func (sc *SpamControl) getSpamCase(ctx context.Context, msg *api.Message) (*db.SpamCase, error) {
	spamCase, err := sc.s.GetDB().GetActiveSpamCase(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		log.WithField("error", err.Error()).Debug("failed to get active spam case")
	}
	if spamCase == nil {
		spamCase, err = sc.s.GetDB().CreateSpamCase(ctx, &db.SpamCase{
			ChatID:      msg.Chat.ID,
			UserID:      msg.From.ID,
			MessageText: bot.ExtractContentFromMessage(msg),
			CreatedAt:   time.Now(),
			Status:      "pending",
		})
		if err != nil {
			log.WithField("error", err.Error()).Debug("failed to create spam case")
			return nil, fmt.Errorf("failed to create spam case: %w", err)
		}
	}
	return spamCase, nil
}

type ProcessingResult struct {
	MessageDeleted bool
	UserBanned     bool
	Error          string
}

func (sc *SpamControl) preprocessMessage(ctx context.Context, msg *api.Message, chat *api.Chat, lang string, voting bool) (*ProcessingResult, error) {
	result := &ProcessingResult{}
	if msg == nil || chat == nil || msg.From == nil {
		return result, nil
	}

	if err := bot.DeleteChatMessage(ctx, sc.s.GetBot(), chat.ID, msg.MessageID); err != nil {
		log.WithField("error", err.Error()).WithField("chat_title", chat.Title).WithField("chat_username", chat.UserName).Error("failed to delete message")
	} else {
		result.MessageDeleted = true
	}

	spamCase, err := sc.getSpamCase(ctx, msg)
	if err != nil {
		return result, err
	}

	if voting {
		if err := sc.banService.MuteUser(ctx, chat.ID, msg.From.ID); err != nil {
			if errors.Is(err, ErrNoPrivileges) {
				result.Error = "CHAT_ADMIN_REQUIRED"
			} else {
				result.Error = err.Error()
			}
		} else {
			result.UserBanned = true
		}
	} else {
		if err := bot.BanUserFromChat(ctx, sc.s.GetBot(), msg.From.ID, chat.ID, 0); err != nil {
			log.WithField("error", err.Error()).WithField("chat_title", chat.Title).WithField("chat_username", chat.UserName).Error("failed to ban user")
			if strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
				result.Error = "CHAT_ADMIN_REQUIRED"
			} else {
				result.Error = err.Error()
			}
		} else {
			result.UserBanned = true
		}
		now := time.Now()
		spamCase.Status = "spam"
		spamCase.ResolvedAt = &now
	}

	if result.Error == "CHAT_ADMIN_REQUIRED" {
		unsuccessReply := api.NewMessage(chat.ID, "I don't have enough rights to ban this user")
		unsuccessReply.ReplyParameters = api.ReplyParameters{
			ChatID:                   chat.ID,
			MessageID:                msg.MessageID,
			AllowSendingWithoutReply: true,
		}
		unsuccessReply.DisableNotification = true
		unsuccessReply.LinkPreviewOptions.IsDisabled = true
		apiResult, err := sc.s.GetBot().Send(unsuccessReply)
		if err != nil {
			log.WithField("error", err.Error()).Error("failed to send unsuccess reply")
		}
		if apiResult.MessageID != 0 {
			time.AfterFunc(sc.config.SuspectNotificationTimeout, func() {
				if _, err := sc.s.GetBot().Request(api.NewDeleteMessage(chat.ID, apiResult.MessageID)); err != nil {
					log.WithField("error", err.Error()).Error("failed to delete unsuccess reply")
				}
			})
		}
	}

	shouldNotify := spamCase.NotificationMessageID == 0 && spamCase.ChannelPostID == 0
	if shouldNotify {
		var notifMsg api.Chattable
		if sc.config.LogChannelUsername != "" {
			channelMsg, err := sc.SendChannelPost(ctx, msg, lang, voting)
			if err != nil {
				log.WithField("error", err.Error()).Error("failed to send channel post")
			}
			if sc.verbose && channelMsg.MessageID != 0 {
				channelPostLink := fmt.Sprintf("https://t.me/%s/%d", sc.config.LogChannelUsername, channelMsg.MessageID)
				notifMsg = sc.createChannelNotification(msg, channelPostLink, lang)
			}
		} else {
			notifMsg = sc.createInChatNotification(msg, spamCase.ID, lang, voting)
		}

		if notifMsg != nil {
			notification, err := sc.s.GetBot().Send(notifMsg)
			if err != nil {
				log.WithField("error", err.Error()).Error("failed to send notification")
			} else {
				spamCase.NotificationMessageID = notification.MessageID

				time.AfterFunc(sc.config.SuspectNotificationTimeout, func() {
					if _, err := sc.s.GetBot().Request(api.NewDeleteMessage(msg.Chat.ID, notification.MessageID)); err != nil {
						log.WithField("error", err.Error()).Error("failed to delete notification")
					}
				})
			}
		}
	}

	if err := sc.s.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		log.WithField("error", err.Error()).Error("failed to update spam case")
	}

	if voting {
		time.AfterFunc(sc.config.VotingTimeoutMinutes, func() {
			if err := sc.ResolveCase(context.Background(), spamCase.ID); err != nil {
				log.WithField("error", err.Error()).Error("failed to resolve spam case")
			}
		})
	}

	return result, nil
}

func (sc *SpamControl) ProcessBannedMessage(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*ProcessingResult, error) {
	return sc.preprocessMessage(ctx, msg, chat, lang, false)
}

func (sc *SpamControl) ProcessSpamMessage(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) (*ProcessingResult, error) {
	return sc.preprocessMessage(ctx, msg, chat, lang, true)
}

func (sc *SpamControl) SendChannelPost(ctx context.Context, msg *api.Message, lang string, voting bool) (*api.Message, error) {
	spamCase, err := sc.getSpamCase(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("failed to get spam case: %w", err)
	}
	channelMsg := sc.createChannelPost(msg, spamCase.ID, lang, voting)
	sent, err := sc.s.GetBot().Send(channelMsg)
	if err != nil {
		log.WithField("error", err.Error()).Error("failed to send channel post")
	}
	spamCase.ChannelUsername = sc.config.LogChannelUsername
	spamCase.ChannelPostID = sent.MessageID
	if err := sc.s.GetDB().UpdateSpamCase(ctx, spamCase); err != nil {
		log.WithField("error", err.Error()).Error("failed to update spam case")
	}

	return &sent, nil
}

func (sc *SpamControl) createInChatNotification(msg *api.Message, caseID int64, lang string, voting bool) api.Chattable {
	text := fmt.Sprintf(i18n.Get("⚠️ Potential spam message from %s\n\nMessage: %s\n\nPlease vote:", lang),
		bot.GetUN(msg.From),
		bot.ExtractContentFromMessage(msg),
	)

	replyMsg := api.NewMessage(msg.Chat.ID, text)
	if voting {
		markup := api.NewInlineKeyboardMarkup(
			api.NewInlineKeyboardRow(
				api.NewInlineKeyboardButtonData("✅ "+i18n.Get("Not Spam", lang), fmt.Sprintf("spam_vote:%d:0", caseID)),
				api.NewInlineKeyboardButtonData("🚫 "+i18n.Get("Spam", lang), fmt.Sprintf("spam_vote:%d:1", caseID)),
			),
		)
		replyMsg.ReplyMarkup = &markup
	}

	replyMsg.ParseMode = api.ModeMarkdown
	replyMsg.DisableNotification = true
	replyMsg.LinkPreviewOptions.IsDisabled = true
	return replyMsg
}

func (sc *SpamControl) createChannelPost(msg *api.Message, caseID int64, lang string, voting bool) api.Chattable {
	from := bot.GetUN(msg.From)
	textSlice := strings.Split(bot.ExtractContentFromMessage(msg), "\n")
	for i, line := range textSlice {
		line = strings.ReplaceAll(line, "http", "_ttp")
		line = strings.ReplaceAll(line, "+7", "+*")

		line = api.EscapeText(api.ModeMarkdownV2, line)
		line = regexp.MustCompile(`@(\w+)`).ReplaceAllString(line, "@**")
		textSlice[i] = line
	}
	text := fmt.Sprintf(i18n.Get(">%s\n**>%s", lang),
		api.EscapeText(api.ModeMarkdownV2, from),
		strings.Join(textSlice, "\n>"),
	)
	channelMsg := api.NewMessageToChannel("@"+strings.TrimPrefix(sc.config.LogChannelUsername, "@"), text)

	if voting {
		markup := api.NewInlineKeyboardMarkup(
			api.NewInlineKeyboardRow(
				api.NewInlineKeyboardButtonData("✅ "+i18n.Get("Not Spam", lang), fmt.Sprintf("spam_vote:%d:0", caseID)),
				api.NewInlineKeyboardButtonData("🚫 "+i18n.Get("Spam", lang), fmt.Sprintf("spam_vote:%d:1", caseID)),
			),
		)
		channelMsg.ReplyMarkup = &markup
	}

	channelMsg.ParseMode = api.ModeMarkdownV2
	return channelMsg
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

func (sc *SpamControl) RecordVote(ctx context.Context, caseID int64, voterID int64, vote bool) (int, int, error) {
	if err := sc.s.GetDB().AddSpamVote(ctx, &db.SpamVote{
		CaseID:  caseID,
		VoterID: voterID,
		Vote:    vote,
		VotedAt: time.Now(),
	}); err != nil {
		return 0, 0, err
	}

	votes, err := sc.s.GetDB().GetSpamVotes(ctx, caseID)
	if err != nil {
		return 0, 0, err
	}

	spamVotes := 0
	notSpamVotes := 0
	for _, v := range votes {
		if v.Vote {
			notSpamVotes++
		} else {
			spamVotes++
		}
	}

	required, err := sc.requiredVoters(ctx, caseID)
	if err != nil {
		return notSpamVotes, spamVotes, err
	}
	if len(votes) >= required {
		if err := sc.ResolveCase(ctx, caseID); err != nil {
			return notSpamVotes, spamVotes, err
		}
	}

	return notSpamVotes, spamVotes, nil
}

func (sc *SpamControl) requiredVoters(ctx context.Context, caseID int64) (int, error) {
	spamCase, err := sc.s.GetDB().GetSpamCase(ctx, caseID)
	if err != nil {
		return 0, err
	}

	members, err := sc.s.GetDB().GetMembers(ctx, spamCase.ChatID)
	if err != nil {
		log.WithField("error", err.Error()).Error("failed to get members count")
	}

	minVotersFromPercentage := int(float64(len(members)) * sc.config.MinVotersPercentage / 100)
	required := max(sc.config.MinVoters, minVotersFromPercentage)
	if sc.config.MaxVoters > 0 && required > sc.config.MaxVoters {
		required = sc.config.MaxVoters
	}
	if required < 1 {
		required = 1
	}

	return required, nil
}

func (sc *SpamControl) ResolveCase(ctx context.Context, caseID int64) error {
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

	requiredVoters, err := sc.requiredVoters(ctx, caseID)
	if err != nil {
		return fmt.Errorf("failed to calculate required voters: %w", err)
	}

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
		if err := bot.BanUserFromChat(ctx, sc.s.GetBot(), spamCase.UserID, spamCase.ChatID, 0); err != nil {
			log.WithField("error", err.Error()).Error("failed to ban user")
		}
	} else {
		if err := sc.banService.UnmuteUser(ctx, spamCase.ChatID, spamCase.UserID); err != nil {
			log.WithField("error", err.Error()).Error("failed to unmute user")
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
