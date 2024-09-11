package handlers

/*
mermaid:
graph BusinessFlow
    A[Start] --> B[Check if Gatekeeper is enabled]
    B --> C{Is Enabled?}
    C -->|Yes| D[Fetch and validate settings]
    C -->|No| E[Return]
    D --> F{Is chat public?}
    F -->|Yes| G[Restrict new member]
    F -->|No| H[Allow new member]
    G --> I[Send challenge message]
    I --> J[Wait for response]
    J -->|Correct| K[Unrestrict member]
    J -->|Incorrect or Timeout| L[Ban member]
    K --> M[Delete challenge message]
    M --> N[Send welcome message]
    N --> O[Remove from challenged users]
    L --> P[Delete challenge message]
    L -->|Ban unsuccessful| Q[Send insufficient permissions message]
    O --> R[End]
    P --> R[End]
    Q --> R[End]
    H --> R[End]
*/
import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/iamwavecut/tool"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/resources"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/pborman/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/i18n"
	"github.com/iamwavecut/ngbot/internal/infra"
)

const (
	captchaSize = 5

	defaultChallengeTimeout = 3 * time.Minute
	defaultRejectTimeout    = 10 * time.Minute

	updateTypeCallbackQuery   updateType = "callback_query"
	updateTypeChatJoinRequest updateType = "chat_join_request"
	updateTypeNewChatMembers  updateType = "new_chat_members"
	updateTypeIgnore          updateType = "ignore"
)

type updateType string

type challengedUser struct {
	user               *api.User
	successFunc        func()
	joinMessageID      int
	challengeMessageID int
	successUUID        string
	targetChat         *api.Chat
	commChat           *api.Chat
}

type Gatekeeper struct {
	s          bot.Service
	joiners    map[int64]map[int64]*challengedUser
	newcomers  map[int64]map[int64]struct{}
	restricted map[int64]map[int64]struct{}

	Variants map[string]map[string]string `yaml:"variants"`
}

var challengeKeys = []string{
	"Hello, %s! We want to be sure you're not a bot, so please select %s. If not, we might have to say goodbye. Thanks for understanding!",
	"Hey %s! To keep this group human-only, could you please choose %s? If you don't, we'll have to say bye-bye. Thanks for your cooperation!",
	"Greetings, %s! We're just checking to make sure you're not a robot. Can you please pick %s? If not, we'll have to let you go. Thanks for your cooperation!",
	"Hi there, %s! We like having humans in this group, so please select %s to prove you're not a bot. If you can't, we might have to remove you. Thanks in advance!",
	"Welcome, %s! We need your help to keep this group human-only. Could you please select %s? If you can't, we might have to remove you. Thanks for your understanding!",
}

var privateChallengeKeys = []string{
	"Hey %s! Exciting to see you're interested in joining the group \"%s\"! We just need one more thing from you to confirm that you're human - pick %s. If you can't, we might have to say goodbye. Thanks for your cooperation!",
	"Hello there, %s! We're happy you want to join \"%s\"! We just need you to pick %s to prove that you're human. If you can't, we might have to remove you. Thanks for your understanding!",
	"Hi %s! We're thrilled you want to be part of \"%s\"! Just one more thing to make sure you're not a bot - please select %s. If you can't, we might have to say goodbye. Thanks for your cooperation!",
	"Welcome, %s! We're glad you're interested in joining \"%s\"! Please pick %s to prove that you're a human being. If you can't, we might have to remove you. Thanks for understanding!",
	"Hey %s! We're excited you want to join the group \"%s\"! Just need a quick test to make sure you're not a robot - pick %s. If you can't, we might have to say bye-bye. Thanks for your cooperation!",
	"Hi there, %s! Joining \"%s\" is fantastic news! Please pick %s to prove you're human. If you can't, we might have to let you go. Thanks for your cooperation!",
	"Hello, %s! We're glad you want to be part of \"%s\"! Just one more step - pick %s to confirm that you're not a bot. If you can't, we might have to say goodbye. Thanks for understanding!",
	"Hey %s! We're excited to see you want to join \"%s\"! To make sure you're not a robot, please select %s. If you can't, we might have to say farewell. Thanks for your cooperation!",
	"Greetings, %s! It's great you want to join \"%s\"! Please pick %s to show that you're human. If you can't, we might have to remove you. Thanks for your understanding!",
	"Hi there, %s! Welcome to the group \"%s\"! We need one more thing from you to confirm that you're human - pick %s. If you can't, we might have to let you go. Thanks for your cooperation!",
}

func NewGatekeeper(s bot.Service) *Gatekeeper {
	entry := log.WithFields(log.Fields{"object": "Gatekeeper", "method": "NewGatekeeper"})
	entry.Debug("creating new gatekeeper")

	g := &Gatekeeper{
		s: s,

		joiners:    map[int64]map[int64]*challengedUser{},
		Variants:   map[string]map[string]string{},
		newcomers:  map[int64]map[int64]struct{}{},
		restricted: map[int64]map[int64]struct{}{},
	}

	for _, lang := range i18n.GetLanguagesList() {
		entry.WithField("language", lang).Debug("loading challenges for language")
		challengesData, err := resources.FS.ReadFile(infra.GetResourcesPath("gatekeeper", "challenges", lang+".yml"))
		if err != nil {
			entry.WithFields(log.Fields{"error": err, "language": lang}).Error("cant load challenges file for language")
		}

		localVariants := map[string]string{}
		if err := yaml.Unmarshal(challengesData, &localVariants); err != nil {
			entry.WithFields(log.Fields{"error": err, "language": lang}).Error("cant unmarshal challenges yaml for language")
		}
		g.Variants[lang] = localVariants
	}
	entry.Debug("Gatekeeper created successfully")
	return g
}

func (g *Gatekeeper) Handle(ctx context.Context, u *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	entry := g.getLogEntry().WithField("method", "Handle")
	entry.Debug("handling update")

	updateType := g.determineUpdateType(u)
	switch updateType {
	case updateTypeIgnore:
		return true, nil
	}

	if chat == nil {
		entry.Debug("Missing chat information")
		return true, nil
	}
	if user == nil {
		entry.Debug("Missing user information")
		return true, nil
	}

	settings, err := g.fetchAndValidateSettings(chat.ID)
	if err != nil {
		return true, err
	}
	if !settings.Enabled {
		entry.Debug("gatekeeper is disabled for this chat")
		return true, nil
	}

	// Handle update based on its type
	switch updateType {
	case updateTypeCallbackQuery:
		return false, g.handleChallenge(ctx, u, chat, user)
	case updateTypeChatJoinRequest:
		return true, g.handleChatJoinRequest(ctx, u)
	// case updateTypeNewChatMembers:
	// return true, g.handleNewChatMembers(ctx, u, chat)
	default:
		entry.Debug("No specific handler matched, proceeding with default behavior")
		return true, nil
	}
}

func (g *Gatekeeper) determineUpdateType(u *api.Update) updateType {
	if u.CallbackQuery != nil {
		return updateTypeCallbackQuery
	}
	if u.ChatJoinRequest != nil {
		return updateTypeChatJoinRequest
	}
	if u.Message != nil {
		if u.Message.NewChatMembers != nil {
			return updateTypeNewChatMembers
		}
	}
	return updateTypeIgnore
}

func (g *Gatekeeper) fetchAndValidateSettings(chatID int64) (*db.Settings, error) {
	entry := g.getLogEntry().WithField("method", "fetchAndValidateSettings")
	entry.Debug("Entering fetchAndValidateSettings method")

	settings, err := g.s.GetDB().GetSettings(chatID)
	if err != nil {
		settings = &db.Settings{
			Enabled:          true,
			ChallengeTimeout: defaultChallengeTimeout,
			RejectTimeout:    defaultRejectTimeout,
			Language:         "en",
			ID:               chatID,
		}
		if err := g.s.GetDB().SetSettings(settings); err != nil {
			return nil, fmt.Errorf("failed to set default chat settings: %w", err)
		}
	}
	return settings, nil
}

func (g *Gatekeeper) handleChallenge(_ context.Context, u *api.Update, chat *api.Chat, user *api.User) (err error) {
	entry := g.getLogEntry().WithField("method", "handleChallenge")
	entry.Debug("handling challenge")
	b := g.s.GetBot()

	cq := u.CallbackQuery
	entry.WithFields(log.Fields{"data": cq.Data, "user": bot.GetUN(user)}).Debug("callback query data")

	joinerID, challengeUUID, err := func(s string) (int64, string, error) {
		entry := g.getLogEntry().WithField("method", "handleChallenge.parseCallbackData")
		entry.WithField("data", s).Debug("parsing callback data")
		parts := strings.Split(s, ";")
		if len(parts) != 2 {
			var errInvalidString = errors.New("invalid string to split")
			entry.WithError(errInvalidString).Error("callback query data is invalid")
			return 0, "", errInvalidString
		}
		ID, err := strconv.ParseInt(parts[0], 10, 0)
		if err != nil {
			var errCantParseUserID = errors.New("cant parse user ID")
			entry.WithError(errCantParseUserID).Error("callback query data is invalid")
			return 0, "", errCantParseUserID
		}
		entry.WithFields(log.Fields{"joinerID": ID, "challengeUUID": parts[1]}).Debug("parsed callback data")
		return ID, parts[1], nil
	}(cq.Data)
	if err != nil {
		entry.WithError(err).Error("failed to parse callback query data")
		return err
	}
	var isAdmin bool

	if chatMember, err := b.GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chat.ID,
			},
			UserID: user.ID,
		},
	}); err == nil {
		user = chatMember.User
		switch {
		case
			chatMember.IsCreator(),
			chatMember.IsAdministrator() && chatMember.CanRestrictMembers:
			isAdmin = true
		}
		entry.WithFields(log.Fields{"user": bot.GetUN(user), "isAdmin": isAdmin}).Debug("user admin status")
	} else {
		entry.WithError(err).Error("Failed to get chat member information")
		return errors.WithMessage(err, "failed to get chat member information")
	}

	lang := g.getLanguage(chat, user)
	entry.WithField("language", lang).Debug("using language")
	cu := g.extractChallengedUser(joinerID, chat.ID)
	if cu == nil {
		entry.Debug("no user matched for challenge")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("This challenge isn't your concern", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}
		return nil
	}
	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: cu.targetChat.ID,
		},
	})
	if err != nil {
		entry.WithError(err).Error("cant get target chat info")
		return errors.WithMessage(err, "cant get target chat info")
	}
	lang = g.getLanguage(&targetChat, user)
	entry.WithField("language", lang).Debug("updated language")

	isPublic := cu.commChat.ID == cu.targetChat.ID
	entry.WithField("isPublic", isPublic).Debug("chat visibility")
	if !isPublic {
		isAdmin = false
	} else {
		entry.WithField("user", bot.GetUN(user)).Info("restricting chatting for user")
		_ = bot.RestrictChatting(b, user.ID, cu.targetChat.ID)
	}

	if !isAdmin && joinerID != user.ID {
		entry.WithField("user", bot.GetUN(user)).Info("user is not admin and not the joiner")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Stop it! You're too real", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}
		return nil
	}

	switch {
	case isAdmin, cu.successUUID == challengeUUID:
		entry.WithField("user", bot.GetUN(cu.user)).Info("successful challenge for user")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("Welcome, friend!", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}

		if _, err = b.Request(api.NewDeleteMessage(cu.commChat.ID, cu.challengeMessageID)); err != nil {
			entry.WithError(err).Error("cant delete challenge message")
		}

		if !isPublic {
			entry.WithField("user", bot.GetUN(cu.user)).Info("approving join request for user")
			_ = bot.ApproveJoinRequest(b, cu.user.ID, cu.targetChat.ID)
			msg := api.NewMessage(cu.commChat.ID, fmt.Sprintf(i18n.Get("Awesome, you're good to go! Feel free to start chatting in the group \"%s\".", lang), api.EscapeText(api.ModeMarkdown, cu.targetChat.Title)))
			msg.ParseMode = api.ModeMarkdown
			_ = tool.Err(b.Send(msg))
		} else {
			entry.WithField("user", bot.GetUN(user)).Info("unrestricting chatting for user")
			_ = bot.UnrestrictChatting(b, user.ID, cu.targetChat.ID)
		}

		if cu.successFunc != nil {
			entry.WithField("user", bot.GetUN(cu.user)).Info("calling success function for user")
			cu.successFunc()
		}
		entry.WithFields(log.Fields{"user": bot.GetUN(cu.user), "chatID": cu.targetChat.ID}).Info("Adding user to newcomers list for chat")
		if _, ok := g.newcomers[cu.targetChat.ID]; !ok {
			entry.WithField("chatID", cu.targetChat.ID).Info("Initializing newcomers map for target chat")
			g.newcomers[cu.targetChat.ID] = map[int64]struct{}{}
		}
		if _, ok := g.newcomers[cu.targetChat.ID][cu.user.ID]; !ok {
			entry.WithFields(log.Fields{"user": bot.GetUN(cu.user), "chatID": cu.targetChat.ID}).Info("Adding user to newcomers list for chat")
			g.newcomers[cu.targetChat.ID][cu.user.ID] = struct{}{}
		}
		entry.WithFields(log.Fields{"user": bot.GetUN(cu.user), "chatID": cu.targetChat.ID}).Info("Adding user to restricted list for chat")
		if _, ok := g.restricted[cu.targetChat.ID]; !ok {
			entry.WithField("chatID", cu.targetChat.ID).Info("Initializing restricted map for target chat")
			g.restricted[cu.targetChat.ID] = map[int64]struct{}{}
		}
		entry.WithFields(log.Fields{"user": bot.GetUN(cu.user), "chatID": cu.commChat.ID}).Info("Removing challenged user from chat")
		g.removeChallengedUser(cu.user.ID, cu.commChat.ID)

	case cu.successUUID != challengeUUID:
		entry.WithField("user", bot.GetUN(cu.user)).Info("failed challenge for user")

		if _, err := b.Request(api.NewCallbackWithAlert(cq.ID, fmt.Sprintf(i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", lang), cu.targetChat.Title, 10))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}

		if !isPublic {
			entry.WithField("user", bot.GetUN(cu.user)).Info("declining join request for user")
			_ = bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChat.ID)
			msg := api.NewMessage(cu.commChat.ID, fmt.Sprintf(i18n.Get("Oops, it looks like you missed the deadline to join \"%s\", but don't worry! You can try again in %s minutes. Keep trying, I believe in you!", lang), cu.targetChat.Title, 10))
			msg.ParseMode = api.ModeMarkdown
			_ = tool.Err(b.Send(msg))
		}
		if cu.joinMessageID != 0 {
			entry.WithFields(log.Fields{"messageID": cu.joinMessageID, "chatID": cu.targetChat.ID}).Info("Deleting join message from chat")
			if err := bot.DeleteChatMessage(b, cu.targetChat.ID, cu.joinMessageID); err != nil {
				entry.WithError(err).Error("cant delete join message")
			}
		}

		entry.WithFields(log.Fields{"messageID": cu.challengeMessageID, "chatID": cu.commChat.ID}).Info("Deleting challenge message from chat")
		if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.challengeMessageID); err != nil {
			entry.WithError(err).Error("cant delete join message")
		}

		entry.WithFields(log.Fields{"user": bot.GetUN(cu.user), "chatID": cu.targetChat.ID}).Info("Banning user from chat")
		if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChat.ID); err != nil {
			entry.WithError(err).Error("cant kick failed")
		}

		// stop timer anyway
		if cu.successFunc != nil {
			entry.WithField("user", bot.GetUN(cu.user)).Info("Calling success function to stop timer for user")
			cu.successFunc()
		}
		entry.WithFields(log.Fields{"user": bot.GetUN(cu.user), "chatID": cu.commChat.ID}).Info("Removing challenged user from chat")
		g.removeChallengedUser(cu.user.ID, cu.commChat.ID)
	default:
		entry.WithField("user", bot.GetUN(cu.user)).Info("Unknown challenge result for user")
		if _, err := b.Request(api.NewCallback(cq.ID, i18n.Get("I have no idea what is going on", lang))); err != nil {
			entry.WithError(err).Error("cant answer callback query")
		}
	}
	return err
}

func (g *Gatekeeper) handleNewChatMembers(ctx context.Context, u *api.Update, chat *api.Chat) error {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "handleNewChatMembers",
		"chat":   chat.Title,
	})
	entry.Info("Handling new chat members")
	return g.handleJoin(ctx, u, u.Message.NewChatMembers, chat, chat)
}

func (g *Gatekeeper) handleChatJoinRequest(ctx context.Context, u *api.Update) error {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "handleChatJoinRequest",
		"chat":   u.ChatJoinRequest.Chat.Title,
	})
	entry.Info("Handling chat join request")
	target := &u.ChatJoinRequest.Chat

	entry.Info("Getting bot chat info")
	comm, err := g.s.GetBot().GetChat(api.ChatInfoConfig{
		ChatConfig: api.ChatConfig{
			ChatID: u.ChatJoinRequest.UserChatID,
		},
	})
	if err != nil {
		entry.WithError(err).Error("Failed to get bot chat info")
		return err
	}

	return g.handleJoin(ctx, u, []api.User{u.ChatJoinRequest.From}, target, &comm)
}

func (g *Gatekeeper) handleJoin(ctx context.Context, u *api.Update, jus []api.User, target *api.Chat, comm *api.Chat) (err error) {
	entry := g.getLogEntry().WithField("method", "handleJoin")
	entry.Debug("Handling join")
	if target == nil || comm == nil {
		entry.Error("Target or comm is nil")
		return errors.New("target or comm is nil")
	}
	b := g.s.GetBot()

	for _, ju := range jus {
		if ju.IsBot {
			entry.WithField("user", bot.GetUN(&ju)).Debug("Skipping bot user")
			continue
		}

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
			UntilDate: time.Now().Add(3 * time.Minute).Unix(),
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
			entry.WithError(err).Error("Failed to restrict user")
		}

		challengeCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)

		cu := &challengedUser{
			user:        &ju,
			successFunc: cancel,
			successUUID: uuid.New(),
			targetChat:  target,
			commChat:    comm,
		}
		if u.Message != nil {
			cu.joinMessageID = u.Message.MessageID
		}
		entry.WithFields(log.Fields{
			"user": bot.GetUN(cu.user),
			"chat": cu.commChat.ID,
		}).Info("Adding challenged user to joiners list")
		if _, ok := g.joiners[cu.commChat.ID]; !ok {
			g.joiners[cu.commChat.ID] = map[int64]*challengedUser{}
		}
		g.joiners[cu.commChat.ID][cu.user.ID] = cu
		commLang := g.getLanguage(cu.commChat, cu.user)

		go func() {
			entry.WithField("user", bot.GetUN(cu.user)).Info("Setting timer")
			timeout := time.NewTimer(3 * time.Minute)
			defer delete(g.joiners[cu.commChat.ID], cu.user.ID)

			select {
			case <-challengeCtx.Done():
				entry.WithField("user", bot.GetUN(cu.user)).Info("Removing challenge timer")
				timeout.Stop()
				return

			case <-timeout.C:
				entry.WithField("user", bot.GetUN(cu.user)).Info("Challenge timed out")
				if cu.challengeMessageID != 0 {
					entry.WithFields(log.Fields{
						"messageID": cu.challengeMessageID,
						"chatID":    cu.commChat.ID,
					}).Info("Deleting challenge message from chat")
					if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.challengeMessageID); err != nil {
						entry.WithError(err).Error("Failed to delete challenge message")
					}
				}
				var errs []error
				if cu.joinMessageID != 0 {
					entry.WithFields(log.Fields{
						"messageID": cu.joinMessageID,
						"chatID":    cu.commChat.ID,
					}).Info("Deleting join message from chat")
					if err := bot.DeleteChatMessage(b, cu.commChat.ID, cu.joinMessageID); err != nil {
						entry.WithError(err).Error("Failed to delete join message")
						errs = append(errs, errors.Wrap(err, "failed to delete message"))
					}
				}
				entry.WithFields(log.Fields{
					"user":   bot.GetUN(cu.user),
					"chatID": cu.targetChat.ID,
				}).Info("Banning user from chat")
				if err := bot.BanUserFromChat(b, cu.user.ID, cu.targetChat.ID); err != nil {
					entry.WithError(err).Error("Failed to ban user")
					errs = append(errs, errors.Wrap(err, "failed to ban user"))
				}

				if len(errs) > 0 {
					lang := g.getLanguage(cu.commChat, cu.user)
					var msgContent string
					if len(errs) == 2 {
						entry.Warn("failed to ban and delete message")
						msgContent = fmt.Sprintf(i18n.Get("I can't delete messages or ban spammer \"%s\".", lang), bot.GetUN(cu.user))
					} else if errors.Is(errs[0], errors.New("failed to delete message")) {
						entry.Warn("failed to delete message")
						msgContent = fmt.Sprintf(i18n.Get("I can't delete messages from spammer \"%s\".", lang), bot.GetUN(cu.user))
					} else {
						entry.Warn("failed to ban joiner")
						msgContent = fmt.Sprintf(i18n.Get("I can't ban new chat member \"%s\".", lang), bot.GetUN(cu.user))
					}
					msgContent += " " + i18n.Get("I should have the permissions to ban and delete messages here.", lang)
					msg := api.NewMessage(cu.commChat.ID, msgContent)
					msg.ParseMode = api.ModeHTML
					if _, err := b.Send(msg); err != nil {
						entry.WithError(err).Error("failed to send message about lack of permissions")
					}
				}
				if cu.commChat.ID != cu.targetChat.ID {
					entry.WithField("user", bot.GetUN(cu.user)).Info("Declining join request")
					if err := bot.DeclineJoinRequest(b, cu.user.ID, cu.targetChat.ID); err != nil {
						entry.WithError(err).Debug("Decline failed")
					}
					entry.WithField("user", bot.GetUN(cu.user)).Info("Sending timeout message")
					sentMsg, err := b.Send(api.NewMessage(cu.commChat.ID, i18n.Get("Your answer is WRONG. Try again in 10 minutes", commLang)))
					if err != nil {
						entry.WithError(err).Error("Failed to send timeout message")
					}
					time.AfterFunc(10*time.Minute, func() {
						entry.WithFields(log.Fields{
							"messageID": sentMsg.MessageID,
							"chatID":    cu.commChat.ID,
						}).Info("Deleting timeout message")
						_ = bot.DeleteChatMessage(b, cu.commChat.ID, sentMsg.MessageID)
					})
				}
				return
			}
		}()

		entry.Debug("Creating captcha buttons")
		buttons, correctVariant := g.createCaptchaButtons(cu, commLang)

		var keys []string
		isPublic := cu.commChat.ID == cu.targetChat.ID
		if isPublic {
			keys = challengeKeys
		} else {
			keys = privateChallengeKeys
		}

		randomKey := keys[tool.RandInt(0, len(keys)-1)]
		nameString := fmt.Sprintf("[%s](tg://user?id=%d) ", api.EscapeText(api.ModeMarkdown, bot.GetFullName(cu.user)), cu.user.ID)

		args := []interface{}{nameString}
		if !isPublic {
			args = append(args, api.EscapeText(api.ModeMarkdown, target.Title))
		}
		args = append(args, correctVariant[1])
		msgText := fmt.Sprintf(i18n.Get(randomKey, commLang), args...)
		msg := api.NewMessage(cu.commChat.ID, msgText)
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
			entry.WithError(err).Error("Failed to send challenge message")
			return errors.WithMessage(err, "cant send")
		}
		cu.challengeMessageID = sentMsg.MessageID
	}
	entry.Debug("Exiting handleChallenge method")
	return nil
}

func (g *Gatekeeper) extractChallengedUser(userID int64, chatID int64) *challengedUser {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "extractChallengedUser",
		"userID": userID,
		"chatID": chatID,
	})
	entry.Debug("Entering method")

	joiner := g.findChallengedUser(userID, chatID)
	if joiner == nil || joiner.user == nil {
		entry.Info("No challenged user found")
		return nil
	}
	entry.WithField("user", bot.GetUN(joiner.user)).Info("Removing challenged user from joiners list")
	delete(g.joiners[chatID], userID)

	entry.Debug("Exiting method")
	return joiner
}

func (g *Gatekeeper) findChallengedUser(userID int64, chatID int64) *challengedUser {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "findChallengedUser",
		"userID": userID,
		"chatID": chatID,
	})
	entry.Debug("Entering method")

	if _, ok := g.joiners[chatID]; !ok {
		entry.Warn("No challenges for chat")
		return nil
	}
	if user, ok := g.joiners[chatID][userID]; ok {
		entry.Info("Challenged user found")
		return user
	}

	entry.Warn("No challenges for chat user")
	entry.Debug("Exiting method")
	return nil
}

func (g *Gatekeeper) removeChallengedUser(userID int64, chatID int64) {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "removeChallengedUser",
		"userID": userID,
		"chatID": chatID,
	})
	entry.Debug("Entering method")

	if _, ok := g.joiners[chatID]; !ok {
		entry.Trace("No challenges for chat")
		return
	}
	if _, ok := g.joiners[chatID][userID]; ok {
		entry.Info("Removing challenged user")
		delete(g.joiners[chatID], userID)
		return
	}
	entry.Trace("No challenges for chat user")
	entry.Debug("Exiting method")
}

func (g *Gatekeeper) createCaptchaIndex(lang string) [][2]string {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "createCaptchaIndex",
		"lang":   lang,
	})
	entry.Debug("Entering method")

	vars := g.Variants[lang]
	captchaIndex := make([][2]string, len(vars))
	idx := 0
	for k, v := range vars {
		captchaIndex[idx] = [2]string{k, v}
		idx++
	}

	entry.Debug("Exiting method")
	return captchaIndex
}

func (g *Gatekeeper) createCaptchaButtons(cu *challengedUser, lang string) ([]api.InlineKeyboardButton, [2]string) {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "createCaptchaButtons",
		"lang":   lang,
	})
	entry.Debug("Entering method")

	captchaIndex := g.createCaptchaIndex(lang)
	captchaRandomSet := make([][2]string, 0, captchaSize)
	usedIDs := make(map[int]struct{}, captchaSize)
	for len(captchaRandomSet) < captchaSize {
		ID := rand.Intn(len(captchaIndex))
		if _, ok := usedIDs[ID]; ok {
			continue
		}
		captchaRandomSet = append(captchaRandomSet, captchaIndex[ID])
		usedIDs[ID] = struct{}{}
	}
	correctVariant := captchaRandomSet[rand.Intn(captchaSize-1)+1]
	var buttons []api.InlineKeyboardButton
	for _, v := range captchaRandomSet {
		result := strconv.FormatInt(cu.user.ID, 10) + ";" + uuid.New()
		if v[0] == correctVariant[0] {
			result = strconv.FormatInt(cu.user.ID, 10) + ";" + cu.successUUID
		}
		buttons = append(buttons, api.NewInlineKeyboardButtonData(v[0], result))
	}

	entry.Debug("Exiting method")
	return buttons, correctVariant
}

func (g *Gatekeeper) getLogEntry() *log.Entry {
	return log.WithField("context", "gatekeeper")
}

func (g *Gatekeeper) getLanguage(chat *api.Chat, user *api.User) string {
	entry := g.getLogEntry().WithFields(log.Fields{
		"method": "getLanguage",
		"chatID": chat.ID,
	})
	entry.Debug("Entering method")

	if settings, err := g.s.GetDB().GetSettings(chat.ID); !tool.Try(err) {
		entry.Debug("Using language from chat settings")
		return settings.Language
	}
	if user != nil && tool.In(user.LanguageCode, i18n.GetLanguagesList()...) {
		entry.Debug("Using language from user settings")
		return user.LanguageCode
	}
	entry.Debug("Using default language")

	entry.Debug("Exiting method")
	return config.Get().DefaultLanguage
}
