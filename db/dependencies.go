package db

type Client interface {
	GetChatMeta(chatID int64) (*ChatMeta, error)
	UpsertChatMeta(chat *ChatMeta) error
	GetCharadeScore(chatID int64, userID int) (*CharadeScore, error)
	GetCharadeStats(chatID int64) ([]*CharadeScore, error)
	AddCharadeScore(chatID int64, userID int) (*CharadeScore, error)
}
