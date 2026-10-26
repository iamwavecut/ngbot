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
)

const (
	globalIndulgenceCommand            = "global_indulgence"
	globalIndulgenceChatID       int64 = 0
	globalIndulgenceListLimit          = 50
	globalIndulgenceActionAdd          = "add"
	globalIndulgenceActionDelete       = "del"
)

func (a *Admin) handleGlobalIndulgenceCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, lang string) (bool, error) {
	if a.ownerUserID == 0 || user.ID != a.ownerUserID || chat.Type != panelChatTypePrivate {
		return true, nil
	}

	text, err := a.globalIndulgenceReply(ctx, strings.Fields(msg.CommandArguments()), user.ID, lang)
	if err != nil {
		return false, err
	}
	reply := api.NewMessage(chat.ID, text)
	reply.DisableNotification = true
	if _, err := bot.Send(ctx, a.bot, reply); err != nil {
		return false, fmt.Errorf("send global indulgence reply: %w", err)
	}
	return false, nil
}

func (a *Admin) globalIndulgenceReply(ctx context.Context, args []string, ownerUserID int64, lang string) (string, error) {
	switch {
	case len(args) == 0:
		return a.listGlobalIndulgence(ctx, lang)
	case len(args) == 2 && args[0] == globalIndulgenceActionAdd:
		reference, err := parseNotSpammerReference(args[1])
		if err != nil {
			return i18n.Get("Invalid input", lang), nil
		}
		if _, err := a.store.CreateGlobalNotSpammerOverride(ctx, &db.ChatNotSpammerOverride{
			ChatID:          globalIndulgenceChatID,
			MatchType:       reference.MatchType,
			MatchValue:      reference.MatchValue,
			CreatedByUserID: ownerUserID,
			CreatedAt:       time.Now(),
		}); err != nil {
			return "", fmt.Errorf("create global indulgence: %w", err)
		}
		return i18n.Get("Global indulgence entry added", lang), nil
	case len(args) == 2 && args[0] == globalIndulgenceActionDelete:
		id, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
		if err != nil || id <= 0 {
			return i18n.Get("Invalid input", lang), nil
		}
		override, err := a.store.GetChatNotSpammerOverride(ctx, globalIndulgenceChatID, id)
		if err != nil {
			return "", fmt.Errorf("get global indulgence: %w", err)
		}
		if override == nil {
			return i18n.Get("Invalid input", lang), nil
		}
		if err := a.store.DeleteChatNotSpammerOverride(ctx, globalIndulgenceChatID, id); err != nil {
			return "", fmt.Errorf("delete global indulgence: %w", err)
		}
		return i18n.Get("Global indulgence entry deleted", lang), nil
	default:
		return i18n.Get("Usage: /global_indulgence add <user ID, @username or t.me link> or /global_indulgence del <entry ID>", lang), nil
	}
}

func (a *Admin) listGlobalIndulgence(ctx context.Context, lang string) (string, error) {
	entries, err := a.store.ListChatNotSpammerOverrides(ctx, globalIndulgenceChatID, globalIndulgenceListLimit, 0)
	if err != nil {
		return "", fmt.Errorf("list global indulgence: %w", err)
	}
	if len(entries) == 0 {
		return i18n.Get("No global indulgence entries yet", lang), nil
	}
	total, err := a.store.CountChatNotSpammerOverrides(ctx, globalIndulgenceChatID)
	if err != nil {
		return "", fmt.Errorf("count global indulgence: %w", err)
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "%s: %d", i18n.Get("Global indulgence", lang), total)
	for _, entry := range entries {
		fmt.Fprintf(&builder, "\n#%d %s", entry.ID, notSpammerReferenceLabel(entry))
	}
	return builder.String(), nil
}
