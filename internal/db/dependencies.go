package db

type Client interface {
	GetUserMeta(userID int64) (*UserMeta, error)
	UpsertUserMeta(chat *UserMeta) error
	GetChatMeta(chatID int64) (*ChatMeta, error)
	UpsertChatMeta(chat *ChatMeta) error
	GetCharadeScore(chatID int64, userID int64) (*CharadeScore, error)
	GetCharadeStats(chatID int64) ([]*CharadeScore, error)
	AddCharadeScore(chatID int64, userID int64) (*CharadeScore, error)
	GetChatLanguage(chatID int64) (string, error)
	SetChatLanguage(chatID int64, lang string) error
}
