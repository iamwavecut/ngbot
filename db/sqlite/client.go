package sqlite

import (
	"database/sql"
	"github.com/iamwavecut/ngbot/db"
	"github.com/iamwavecut/ngbot/infra"
	"github.com/pkg/errors"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
	migrate "github.com/rubenv/sql-migrate"
	log "github.com/sirupsen/logrus"
)

var (
	ErrNoUser = errors.New("no user in db")
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

func (c *sqliteClient) GetUserMeta(userID int) (*db.UserMeta, error) {
	res := &db.UserMeta{}
	if err := c.db.Get(res, "SELECT * FROM users WHERE id=?", userID); err != nil {
		if errors.Cause(err) == sql.ErrNoRows {
			return nil, ErrNoUser
		}
		return nil, errors.WithMessage(err, "cant get user meta")
	}
	return res, nil
}

func (c *sqliteClient) UpsertUserMeta(chat *db.UserMeta) error {
	query := `
		INSERT INTO users (id, first_name, last_name, username, language_code, is_bot) VALUES(:id, :first_name, :last_name, :username, :language_code,:is_bot)
		ON CONFLICT(id) DO UPDATE SET first_name=excluded.first_name, last_name=excluded.last_name, username=excluded.username, language_code=excluded.language_code, is_bot=excluded.is_bot;
	`
	if _, err := c.db.NamedExec(query, chat); err != nil {
		return errors.WithMessage(err, "cant insert user meta")
	}
	return nil
}

func (c *sqliteClient) GetChatMeta(chatID int64) (*db.ChatMeta, error) {
	res := &db.ChatMeta{}
	if err := c.db.Get(res, "SELECT * FROM chats WHERE id=?", chatID); err != nil {
		switch errors.Cause(err) {
		case sql.ErrNoRows:
			res = &db.ChatMeta{
				ID:       chatID,
				Title:    "",
				Language: "en",
				Type:     "",
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
	query := `
		INSERT INTO chats (id, title, language, type) VALUES(:id, :title, :language, :type)
		ON CONFLICT(id) DO UPDATE SET title=excluded.title, language=excluded.language, type=excluded.type;;
	`
	if _, err := c.db.NamedExec(query, chat); err != nil {
		return errors.WithMessage(err, "cant insert chat meta")
	}
	return nil
}

func (c *sqliteClient) GetCharadeScore(chatID int64, userID int) (*db.CharadeScore, error) {
	var res db.CharadeScore
	query := `
		SELECT user_id, chat_id, score 
		FROM charade_scores 
		WHERE user_id = CAST(? AS INT) AND chat_id=CAST(? AS BIGINT)`
	return &res, c.db.Get(&res, query, userID, chatID)
}

func (c *sqliteClient) GetCharadeStats(chatID int64) ([]*db.CharadeScore, error) {
	res := make([]*db.CharadeScore, 0)
	query := `
		SELECT user_id, chat_id, score
		FROM charade_scores
		WHERE chat_id=CAST(? AS BIGINT)`
	return res, c.db.Select(&res, query, chatID)
}

func (c *sqliteClient) AddCharadeScore(chatID int64, userID int) (*db.CharadeScore, error) {
	if _, err := c.db.Exec(`
		INSERT INTO charade_scores (user_id, chat_id, score) VALUES(?, ?, 1)
		ON CONFLICT(user_id, chat_id) DO UPDATE SET score=score+1;
	`, userID, chatID); err != nil {
		return nil, errors.WithMessage(err, "cant add charade score")
	}
	return c.GetCharadeScore(chatID, userID)
}
