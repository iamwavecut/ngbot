package db

type Client interface {
	SetSettings(settings *Settings) error
	GetSettings(chatID int64) (*Settings, error)
	
	IsMigrated(chatID int64) (bool, error)
	SetMigrated(chatID int64, migrated bool) error

	InsertMember(chatID int64, userID int64) error
	InsertMembers(chatID int64, userIDs []int64) error
	DeleteMember(chatID int64, userID int64) error
	DeleteMembers(chatID int64, userIDs []int64) error
	GetMembers(chatID int64) ([]int64, error)
}
