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
	"github.com/iamwavecut/ngbot/internal/policy/permissions"
)

func (a *Admin) ensureManagerAccess(ctx context.Context, chatID int64, userID int64, callbackID string) error {
	membership, err := a.store.GetChatBotMembership(ctx, chatID)
	if err == nil && membership != nil && !membership.IsMember {
		if callbackID != "" {
			a.answerCallback(ctx, callbackID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, nil)))
		}
		return fmt.Errorf("bot is not member")
	}

	member, err := a.getChatMember(ctx, chatID, userID)
	if err != nil {
		a.handleBotMembershipError(ctx, chatID, err)
		if callbackID != "" {
			a.answerCallback(ctx, callbackID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, nil)))
		}
		return err
	}
	if !permissions.IsManager(member) {
		if callbackID != "" {
			a.answerCallback(ctx, callbackID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, nil)))
		}
		return fmt.Errorf("no access")
	}
	return a.upsertManager(ctx, chatID, userID, member)
}

func (a *Admin) startTyping(ctx context.Context, chatID int64) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(panelTypingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = a.s.GetBot().Request(api.NewChatAction(chatID, api.ChatTyping))
			}
		}
	}()
	_, _ = a.s.GetBot().Request(api.NewChatAction(chatID, api.ChatTyping))
	return func() {
		close(stop)
	}
}

func (a *Admin) sendPlaceholder(ctx context.Context, chatID int64, language string) (api.Message, error) {
	msg := api.NewMessage(chatID, i18n.Get("Please wait...", language))
	msg.DisableNotification = true
	return a.s.GetBot().Send(msg)
}

func (a *Admin) editMessage(ctx context.Context, chatID int64, messageID int, text string, markup *api.InlineKeyboardMarkup) error {
	edit := api.NewEditMessageText(chatID, messageID, text)
	edit.ReplyMarkup = markup
	_, err := a.s.GetBot().Send(edit)
	return err
}

func (a *Admin) answerCallback(ctx context.Context, callbackID string, text string) {
	_, _ = a.s.GetBot().Request(api.NewCallback(callbackID, text))
}

func (a *Admin) getChatMember(ctx context.Context, chatID int64, userID int64) (*api.ChatMember, error) {
	member, err := a.s.GetBot().GetChatMember(api.GetChatMemberConfig{
		ChatConfigWithUser: api.ChatConfigWithUser{
			ChatConfig: api.ChatConfig{
				ChatID: chatID,
			},
			UserID: userID,
		},
	})
	if err != nil {
		return nil, err
	}
	return &member, nil
}

func (a *Admin) handleBotMembershipError(ctx context.Context, chatID int64, err error) {
	if err == nil {
		return
	}
	if isBotRemovedError(err) {
		_ = a.store.SetChatBotMembership(ctx, &db.ChatBotMembership{
			ChatID:    chatID,
			IsMember:  false,
			UpdatedAt: time.Now(),
		})
	}
}

func (a *Admin) deleteGroupMessage(ctx context.Context, chatID int64, messageID int) error {
	err := bot.DeleteChatMessage(ctx, a.s.GetBot(), chatID, messageID)
	if isBotRemovedError(err) {
		_ = a.store.SetChatBotMembership(ctx, &db.ChatBotMembership{
			ChatID:    chatID,
			IsMember:  false,
			UpdatedAt: time.Now(),
		})
	}
	return err
}

func (a *Admin) upsertManager(ctx context.Context, chatID int64, userID int64, member *api.ChatMember) error {
	manager := &db.ChatManager{
		ChatID:             chatID,
		UserID:             userID,
		CanManageChat:      member.CanManageChat,
		CanPromoteMembers:  member.CanPromoteMembers,
		CanRestrictMembers: member.CanRestrictMembers,
		UpdatedAt:          time.Now(),
	}
	return a.store.UpsertChatManager(ctx, manager)
}

func isBotRemovedError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "forbidden") || strings.Contains(errText, "kicked") || strings.Contains(errText, "chat not found")
}

func (a *Admin) handleGroupDeleteCallback(ctx context.Context, cq *api.CallbackQuery, user *api.User) error {
	payload := strings.TrimPrefix(cq.Data, "del_")
	encodedChatID, encodedMessageID, ok := splitDeletePayload(payload)
	if !ok {
		return nil
	}
	chatID, err := decodeChatID(encodedChatID)
	if err != nil {
		return nil
	}
	commandMessageID, err := decodeMessageID(encodedMessageID)
	if err != nil {
		return nil
	}

	member, err := a.getChatMember(ctx, chatID, user.ID)
	if err != nil {
		a.handleBotMembershipError(ctx, chatID, err)
		return nil
	}
	if !permissions.IsPrivilegedModerator(member) {
		a.answerCallback(ctx, cq.ID, i18n.Get("No access", a.s.GetLanguage(ctx, chatID, user)))
		return nil
	}

	if cq.Message != nil {
		_ = a.deleteGroupMessage(ctx, chatID, cq.Message.MessageID)
	}
	_ = a.deleteGroupMessage(ctx, chatID, commandMessageID)
	a.answerCallback(ctx, cq.ID, "")
	return nil
}
