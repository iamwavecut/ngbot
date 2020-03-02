package db

type Client interface {
	GetChatMeta(chatID int64) (*ChatMeta, error)
	UpsertChatMeta(chat *ChatMeta) error
}
