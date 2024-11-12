package db

import "context"

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
}
