package sqlite

import (
	"database/sql"
	"github.com/iamwavecut/ngbot/db"
	"github.com/iamwavecut/ngbot/infra"
	"github.com/pkg/errors"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"
	log "github.com/sirupsen/logrus"
)

type sqliteClient struct {
	db *sqlx.DB
}

func NewSQLiteClient(dbPath string) *sqliteClient {
	dbx, err := sqlx.Open("sqlite3", filepath.Join(infra.GetWorkDir(), dbPath))
	if err != nil {
		log.WithError(err).Fatalln("cant open db")
	}
	dbx.SetMaxOpenConns(42)

	n, err := migrate.Exec(dbx.DB, "sqlite3", &migrate.FileMigrationSource{
		Dir: infra.GetResourcesDir("migrations"),
	}, migrate.Up)
	if err != nil {
		log.WithError(err).Fatalln("migrate up failed")
	}
	log.Infof("applied %d migrations!\n", n)

	return &sqliteClient{db: dbx}
}

func (c *sqliteClient) GetChatMeta(chatID int64) (*db.ChatMeta, error) {
	var res *db.ChatMeta
	if err := c.db.Get(res, "SELECT * FROM chats WHERE id=$1", chatID); err != nil {
		switch errors.Cause(err) {
		case sql.ErrNoRows:
			res = &db.ChatMeta{
				ID:       chatID,
				Title:    "",
				Language: "en",
			}
			if err := c.UpsertChatMeta(res); err != nil {
				log.WithError(err).Error("default insert failed")
			}
		default:
			return nil, errors.WithMessage(err, "cant get chat meta")
		}
	}
	return res, nil
}

func (c *sqliteClient) UpsertChatMeta(chat *db.ChatMeta) error {
	if _, err := c.db.NamedExec(`
INSERT INTO chats (id, title, language) VALUES(:id, :title, :language)
ON CONFLICT(word) DO UPDATE SET title=excluded.title, language=excluded.language;
`, chat); err != nil {
		return errors.WithMessage(err, "cant insert chat meta")
	}
	return nil
}
