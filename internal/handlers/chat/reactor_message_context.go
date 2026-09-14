package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

const (
	messageContextRetention    = 24 * time.Hour
	messageContextTextLimit    = 2000
	messageContextTotalLimit   = 8000
	messageContextHistoryLimit = 5
)

type messageContextUpdateKey struct{}

func withMessageContextUpdate(ctx context.Context, update *api.Update) context.Context {
	if update == nil {
		return ctx
	}
	return context.WithValue(ctx, messageContextUpdateKey{}, update.UpdateID)
}

type contextChatInfo struct {
	plainGroup   bool
	linkedChatID int64
	expiresAt    time.Time
}

func (r *Reactor) rememberMessageContext(ctx context.Context, msg *api.Message, chat *api.Chat, settings *db.Settings) error {
	if msg == nil || chat == nil || r.store == nil || (settings != nil && !settings.LLMFirstMessageEnabled) {
		return nil
	}
	if reply := msg.ReplyToMessage; reply != nil && reply.Chat.ID == chat.ID {
		if err := r.saveMessageContext(context.WithValue(ctx, messageContextUpdateKey{}, 0), reply, chat); err != nil {
			return err
		}
	}
	return r.saveMessageContext(ctx, msg, chat)
}

func (r *Reactor) saveMessageContext(ctx context.Context, msg *api.Message, chat *api.Chat) error {
	author, ok := bot.MessageAuthor(msg)
	if !ok || msg.MessageID <= 0 {
		return nil
	}
	sentAt := r.currentTime()
	if msg.Date != 0 {
		sentAt = time.Unix(msg.Date, 0).UTC()
	}
	updatedAt := sentAt
	if msg.EditDate != 0 {
		updatedAt = time.Unix(msg.EditDate, 0).UTC()
	}
	threadID := msg.MessageThreadID
	if msg.IsAutomaticForward {
		threadID = msg.MessageID
	}
	replyID := 0
	if reply := msg.ReplyToMessage; reply != nil && reply.Chat.ID == chat.ID {
		replyID = reply.MessageID
		if threadID == 0 {
			parent, err := r.store.MessageContext(ctx, chat.ID, replyID)
			if err != nil {
				return fmt.Errorf("read reply thread: %w", err)
			}
			if parent != nil {
				threadID = parent.ThreadID
			}
		}
	}
	updateID, _ := ctx.Value(messageContextUpdateKey{}).(int)
	return r.store.UpsertMessageContext(ctx, &db.MessageContext{
		ChatID: chat.ID, MessageID: msg.MessageID, ThreadID: threadID, ReplyToMessageID: replyID,
		AuthorKind: author.Kind, AuthorID: author.ID, Text: bot.ExtractTextFromMessage(msg), SentAt: sentAt, UpdatedAt: updatedAt, UpdateID: updateID,
	})
}

func (r *Reactor) messageConversation(ctx context.Context, msg *api.Message, chat *api.Chat) ([]moderation.ConversationMessage, error) {
	if r.store == nil {
		return nil, nil
	}
	cutoff := r.currentTime().Add(-messageContextRetention)
	result := make([]moderation.ConversationMessage, 0, messageContextHistoryLimit+2)
	seen := map[int]bool{msg.MessageID: true}
	remaining := messageContextTotalLimit
	threadID, replyID := msg.MessageThreadID, 0
	appendText := func(role, text string) {
		text = strings.TrimSpace(text)
		if text == "" || remaining <= 0 {
			return
		}
		runes := []rune(text)
		limit := min(messageContextTextLimit, remaining)
		if len(runes) > limit {
			runes = runes[:limit]
		}
		remaining -= len(runes)
		result = append(result, moderation.ConversationMessage{Role: role, Message: string(runes)})
	}
	appendRecord := func(role string, record *db.MessageContext) {
		if record == nil || seen[record.MessageID] || record.ChatID != chat.ID || record.SentAt.Before(cutoff) || (threadID != 0 && record.ThreadID != threadID && record.MessageID != threadID) {
			return
		}
		seen[record.MessageID] = true
		appendText(role, record.Text)
	}
	current, err := r.store.MessageContext(ctx, chat.ID, msg.MessageID)
	if err != nil {
		return nil, err
	}
	if current != nil {
		replyID = current.ReplyToMessageID
		if threadID == 0 {
			threadID = current.ThreadID
		}
	}
	if reply := msg.ReplyToMessage; reply != nil && reply.Chat.ID == chat.ID {
		replyID = reply.MessageID
	}
	var reply *db.MessageContext
	if replyID != 0 {
		reply, err = r.store.MessageContext(ctx, chat.ID, replyID)
		if err != nil {
			return nil, err
		}
		if threadID == 0 && reply != nil {
			threadID = reply.ThreadID
		}
	}
	quoteInThread := reply != nil && reply.ChatID == chat.ID && !reply.SentAt.Before(cutoff) && (threadID == 0 || reply.ThreadID == threadID || reply.MessageID == threadID)
	if parent := msg.ReplyToMessage; parent != nil && threadID != 0 && parent.MessageThreadID != 0 && parent.MessageThreadID != threadID {
		quoteInThread = false
	}
	if msg.Quote != nil && quoteInThread {
		appendText("quote", msg.Quote.Text)
	}
	appendRecord("direct_reply", reply)
	if threadID != 0 {
		root, err := r.store.MessageContext(ctx, chat.ID, threadID)
		if err != nil {
			return nil, err
		}
		appendRecord("original_post", root)
	}
	previous, err := r.store.RecentMessageContext(ctx, chat.ID, threadID, msg.MessageID, cutoff, messageContextHistoryLimit+2)
	if err != nil {
		return nil, err
	}
	useRecent := threadID != 0
	if !useRecent && len(previous) > 0 {
		useRecent = r.isPlainContextGroup(ctx, chat)
	}
	if useRecent {
		count := 0
		for i := range previous {
			if seen[previous[i].MessageID] {
				continue
			}
			appendRecord("previous_reply", &previous[i])
			count++
			if count == messageContextHistoryLimit {
				break
			}
		}
	} else {
		for range messageContextHistoryLimit {
			if reply == nil || reply.ReplyToMessageID == 0 || seen[reply.ReplyToMessageID] {
				break
			}
			reply, err = r.store.MessageContext(ctx, chat.ID, reply.ReplyToMessageID)
			if err != nil {
				return nil, err
			}
			appendRecord("previous_reply", reply)
		}
	}
	return result, nil
}

func (r *Reactor) isPlainContextGroup(ctx context.Context, chat *api.Chat) bool {
	if chat.IsGroup() {
		return true
	}
	if chat.IsForum {
		return false
	}
	info, err := r.messageChatInfo(ctx, chat)
	if err != nil {
		r.getLogEntry().WithError(err).WithField(logFieldChatID, chat.ID).Warn("cannot establish context group scope; using reply chain only")
		return false
	}
	return info.plainGroup
}

func (r *Reactor) messageChatInfo(ctx context.Context, chat *api.Chat) (contextChatInfo, error) {
	now := r.currentTime()
	r.contextChatMutex.Lock()
	info, found := r.contextChats[chat.ID]
	r.contextChatMutex.Unlock()
	if found && now.Before(info.expiresAt) {
		return info, nil
	}
	fullChat, err := bot.GetChat(ctx, r.bot, api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: chat.ID}})
	if err != nil {
		return contextChatInfo{}, err
	}
	info = contextChatInfo{plainGroup: fullChat.LinkedChatID == 0 && !fullChat.IsForum, linkedChatID: fullChat.LinkedChatID, expiresAt: now.Add(5 * time.Minute)}
	r.contextChatMutex.Lock()
	if r.contextChats == nil {
		r.contextChats = make(map[int64]contextChatInfo)
	}
	for id, cached := range r.contextChats {
		if !now.Before(cached.expiresAt) {
			delete(r.contextChats, id)
		}
	}
	r.contextChats[chat.ID] = info
	r.contextChatMutex.Unlock()
	return info, nil
}
