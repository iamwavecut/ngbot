package db

type ChatMeta struct {
	ID       int64  `db:"id"`
	Title    string `db:"title"`
	Language string `db:"language"`
}
