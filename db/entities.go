package db

type ChatMeta struct {
	ID       int64  `db:"id"`
	Title    string `db:"title"`
	Language string `db:"language"`
	Type     string `db:"type"`
}

type CharadeScore struct {
	UserID int   `db:"user_id"`
	ChatID int64 `db:"chat_id"`
	Score  int   `db:"score"`
}
