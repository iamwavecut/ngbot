package handlers

import (
	"context"
	"fmt"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

func (r *Reactor) trustAdminAddedMembers(ctx context.Context, update *api.Update) error {
	var chat *api.Chat
	var actor *api.User
	var members []api.User
	var date int64
	switch {
	case update.ChatMember != nil && isChatMemberJoinTransition(update.ChatMember):
		change := update.ChatMember
		if change.NewChatMember.User == nil || change.ViaChatFolderInviteLink {
			return nil
		}
		chat, actor, date = &change.Chat, &change.From, change.Date
		members = []api.User{*change.NewChatMember.User}
	case update.Message != nil && len(update.Message.NewChatMembers) != 0 && update.Message.SenderChat == nil:
		message := update.Message
		chat, actor, date, members = &message.Chat, message.From, message.Date, message.NewChatMembers
	default:
		return nil
	}
	if actor == nil || actor.ID <= 0 || actor.IsBot || actor.ID == r.bot.Self.ID || chat.ID >= 0 || date <= 0 {
		return nil
	}
	eligible := make([]api.User, 0, len(members))
	for _, member := range members {
		if member.ID > 0 && !member.IsBot && member.ID != actor.ID {
			eligible = append(eligible, member)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	administrator, err := bot.GetChatMember(ctx, r.bot, api.GetChatMemberConfig{ChatConfigWithUser: api.ChatConfigWithUser{ChatConfig: api.ChatConfig{ChatID: chat.ID}, UserID: actor.ID}})
	if err != nil {
		return fmt.Errorf("verify administrator who added members: %w", err)
	}
	if !administrator.IsCreator() && !administrator.IsAdministrator() {
		return nil
	}
	if _, err := r.getOrCreateSettings(ctx, chat); err != nil {
		return err
	}
	for _, member := range eligible {
		if err := r.store.GrantAdminMemberTrust(ctx, chat.ID, member.ID, time.Unix(date, 0), r.authorTrustDuration()); err != nil {
			return err
		}
		r.getLogEntry().WithFields(log.Fields{logFieldChatID: chat.ID, logFieldUserID: member.ID, "administrator_id": actor.ID, "admitted_at": date}).Info("administrator admission recorded")
	}
	return nil
}

func (g *Gatekeeper) skipAdminAdmittedChallenge(ctx context.Context, chatID, userID int64) (bool, error) {
	trust, err := g.store.MessageTrust(ctx, chatID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: userID})
	if err != nil || trust == nil || !trust.AdminAdmittedAt.Valid || !trust.Trusted(g.currentTime()) {
		return false, err
	}
	challenge, err := g.store.GetChallengeByChatUser(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	if challenge != nil {
		if err := g.cleanupChallengeWithoutPenalty(ctx, challenge); err != nil {
			return false, err
		}
	}
	return true, nil
}
