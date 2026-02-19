package db

import (
	"context"
	"time"
)

type Client interface {
	Close() error
	SetSettings(ctx context.Context, settings *Settings) error
	GetSettings(ctx context.Context, chatID int64) (*Settings, error)
	GetAllSettings(ctx context.Context) (map[int64]*Settings, error)
	InsertMember(ctx context.Context, chatID int64, userID int64) error
	InsertMembers(ctx context.Context, chatID int64, userIDs []int64) error
	DeleteMember(ctx context.Context, chatID int64, userID int64) error
	DeleteMembers(ctx context.Context, chatID int64, userIDs []int64) error
	GetMembers(ctx context.Context, chatID int64) ([]int64, error)
	GetAllMembers(ctx context.Context) (map[int64][]int64, error)
	IsMember(ctx context.Context, chatID int64, userID int64) (bool, error)

	// Gatekeeper challenges
	CreateChallenge(ctx context.Context, challenge *Challenge) (*Challenge, error)
	GetChallengeByMessage(ctx context.Context, commChatID, userID int64, challengeMessageID int) (*Challenge, error)
	UpdateChallenge(ctx context.Context, challenge *Challenge) error
	DeleteChallenge(ctx context.Context, commChatID, userID, chatID int64) error
	GetExpiredChallenges(ctx context.Context, now time.Time) ([]*Challenge, error)

	// KV store methods
	GetKV(ctx context.Context, key string) (string, error)
	SetKV(ctx context.Context, key string, value string) error

	// Spam tracking methods
	AddRestriction(ctx context.Context, restriction *UserRestriction) error
	GetActiveRestriction(ctx context.Context, chatID, userID int64) (*UserRestriction, error)
	RemoveRestriction(ctx context.Context, chatID, userID int64) error
	RemoveExpiredRestrictions(ctx context.Context) error

	// Spam control methods
	CreateSpamCase(ctx context.Context, sc *SpamCase) (*SpamCase, error)
	UpdateSpamCase(ctx context.Context, sc *SpamCase) error
	GetSpamCase(ctx context.Context, id int64) (*SpamCase, error)
	GetPendingSpamCases(ctx context.Context) ([]*SpamCase, error)
	GetActiveSpamCase(ctx context.Context, chatID int64, userID int64) (*SpamCase, error)
	AddSpamVote(ctx context.Context, vote *SpamVote) error
	GetSpamVotes(ctx context.Context, caseID int64) ([]*SpamVote, error)
	AddChatRecentJoiner(ctx context.Context, joiner *RecentJoiner) (*RecentJoiner, error)
	GetChatRecentJoiners(ctx context.Context, chatID int64) ([]*RecentJoiner, error)
	GetUnprocessedRecentJoiners(ctx context.Context) ([]*RecentJoiner, error)
	ProcessRecentJoiner(ctx context.Context, chatID int64, userID int64, isSpammer bool) error
	UpsertBanlist(ctx context.Context, userIDs []int64) error
	GetBanlist(ctx context.Context) (map[int64]struct{}, error)

	// Chat managers
	UpsertChatManager(ctx context.Context, manager *ChatManager) error
	GetChatManager(ctx context.Context, chatID int64, userID int64) (*ChatManager, error)

	// Bot membership
	SetChatBotMembership(ctx context.Context, membership *ChatBotMembership) error
	GetChatBotMembership(ctx context.Context, chatID int64) (*ChatBotMembership, error)

	// Admin panel sessions
	CreateAdminPanelSession(ctx context.Context, session *AdminPanelSession) (*AdminPanelSession, error)
	GetAdminPanelSession(ctx context.Context, id int64) (*AdminPanelSession, error)
	GetAdminPanelSessionByUserChat(ctx context.Context, userID int64, chatID int64) (*AdminPanelSession, error)
	GetAdminPanelSessionByUserPage(ctx context.Context, userID int64, page string) (*AdminPanelSession, error)
	UpdateAdminPanelSession(ctx context.Context, session *AdminPanelSession) error
	DeleteAdminPanelSession(ctx context.Context, id int64) error
	GetExpiredAdminPanelSessions(ctx context.Context, before time.Time) ([]*AdminPanelSession, error)

	// Admin panel commands
	CreateAdminPanelCommand(ctx context.Context, cmd *AdminPanelCommand) (*AdminPanelCommand, error)
	GetAdminPanelCommand(ctx context.Context, id int64) (*AdminPanelCommand, error)
	DeleteAdminPanelCommandsBySession(ctx context.Context, sessionID int64) error

	// Spam examples
	CreateChatSpamExample(ctx context.Context, example *ChatSpamExample) (*ChatSpamExample, error)
	GetChatSpamExample(ctx context.Context, id int64) (*ChatSpamExample, error)
	ListChatSpamExamples(ctx context.Context, chatID int64, limit int, offset int) ([]*ChatSpamExample, error)
	CountChatSpamExamples(ctx context.Context, chatID int64) (int, error)
	DeleteChatSpamExample(ctx context.Context, id int64) error
}
