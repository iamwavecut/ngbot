package handlers

import (
	"context"

	"github.com/iamwavecut/ngbot/internal/db"
)

type BanlistPrecheck struct {
	ChatID           int64
	UserID           int64
	Username         string
	AllowlistChecked bool
	Allowlisted      bool
	ProviderChecked  bool
}

type banlistPrecheckContextKey struct{}

func WithBanlistPrecheck(ctx context.Context, precheck BanlistPrecheck) context.Context {
	return context.WithValue(ctx, banlistPrecheckContextKey{}, precheck)
}

func BanlistPrecheckFromContext(ctx context.Context) (BanlistPrecheck, bool) {
	precheck, ok := ctx.Value(banlistPrecheckContextKey{}).(BanlistPrecheck)
	return precheck, ok
}

func (p BanlistPrecheck) CoversIdentity(chatID, userID int64, username string) bool {
	return p.ChatID == chatID && p.UserID == userID && db.NormalizeChatNotSpammerUsername(p.Username) == db.NormalizeChatNotSpammerUsername(username)
}
