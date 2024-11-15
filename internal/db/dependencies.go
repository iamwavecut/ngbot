package db

import (
	"context"
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
}
