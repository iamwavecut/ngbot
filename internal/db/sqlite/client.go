package sqlite

import (
	"context"
	"os"
	"path/filepath"

	"github.com/iamwavecut/tool"
	"github.com/pkg/errors"

	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/infra"
	"github.com/iamwavecut/ngbot/resources"

	"github.com/jmoiron/sqlx"
	migrate "github.com/rubenv/sql-migrate"
	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

type sqliteClient struct {
	db *sqlx.DB
}

func NewSQLiteClient(dbPath string) *sqliteClient {
	dbx, err := sqlx.Open("sqlite", filepath.Join(infra.GetWorkDir(), dbPath))
	if err != nil {
		log.WithError(err).Fatalln("cant open db")
	}
	dbx.SetMaxOpenConns(42)

	postProcessMigrations := map[string]func(){
		"1637265145-add-chats-settings.sql": func() {
			var chats []db.ChatMeta
			err := dbx.Select(&chats, `select * from chats;`)
			if err != nil {
				log.WithError(err).Fatalln("cant select chats, postprocess aborted (CRITICAL)")
			}
			for i, chat := range chats {
				lang, err := chat.GetLanguage()
				if err != nil {
					lang = chat.Language
				}
				if lang == "" {
					lang = config.Get().DefaultLanguage
				}
				chats[i].Settings["language"] = lang
			}
			tx, err := dbx.BeginTxx(context.Background(), nil)
			if err != nil {
				log.WithError(err).Fatalln("cant create tx, postprocess aborted (CRITICAL)")
			}
			stmt, err := tx.PrepareNamed("update chats set language=null, settings=:settings where id = :id")
			if err != nil {
				log.WithError(err).Errorln("cant create named statement, postprocess aborted (CRITICAL)")
				if err := tx.Rollback(); err != nil {
					log.WithError(err).Fatalln("cant rollback")
				}
				os.Exit(1)
			}
			for _, chat := range chats {
				res, err := stmt.Exec(chat)
				if err != nil {
					log.WithError(err).Errorln("cant exec named statement, postprocess aborted (CRITICAL)")
					if err := tx.Rollback(); err != nil {
						log.WithError(err).Fatalln("cant rollback")
					}
					os.Exit(1)
				}
				if _, err = res.RowsAffected(); err != nil {
					log.WithError(err).Errorln("cant get affected rows, postprocess aborted (CRITICAL)")
					if err := tx.Rollback(); err != nil {
						log.WithError(err).Fatalln("cant rollback")
					}
					os.Exit(1)
				}
			}
			log.Infof("chats updated: %d", len(chats))
		},
	}

	migrationsSource := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: resources.FS,
		Root:       "migrations",
	}
	plan, _, err := migrate.PlanMigration(dbx.DB, "sqlite3", migrationsSource, migrate.Up, 0)
	if err != nil {
		log.WithError(err).Fatalln("migrate plan failed")
	}

	n, err := migrate.Exec(dbx.DB, "sqlite3", migrationsSource, migrate.Up)
	if err != nil {
		log.WithError(err).Fatalln("migrate up failed")
	}
	if n > 0 {
		log.Infof("applied %d migrations!", n)
	}
	if n == len(plan) {
		for _, step := range plan {
			if callback, ok := postProcessMigrations[step.Id]; ok {
				log.Infof("postprocessing migration: %s", step.Id)
				callback()
			}
		}
	}

	return &sqliteClient{db: dbx}
}

func (c *sqliteClient) GetUserMeta(userID int64) (*db.UserMeta, error) {
	res := &db.UserMeta{}
	return res, c.db.Get(res, "select * from users where id=?", userID)
}

func (c *sqliteClient) UpsertUserMeta(chat *db.UserMeta) error {
	query := `
		insert into users (id, first_name, last_name, username, language_code, is_bot) values(:id, :first_name, :last_name, :username, :language_code,:is_bot)
		on conflict(id) do update set first_name=excluded.first_name, last_name=excluded.last_name, username=excluded.username, language_code=excluded.language_code, is_bot=excluded.is_bot;
	`
	if _, err := c.db.NamedExec(query, chat); err != nil {
		return errors.WithMessage(err, "cant insert user meta")
	}
	return nil
}

func (c *sqliteClient) GetChatMeta(chatID int64) (*db.ChatMeta, error) {
	res := &db.ChatMeta{}
	return res, c.db.Get(res, "select * from chats where id=?", chatID)
}

func (c *sqliteClient) GetChatLanguage(chatID int64) (string, error) {
	var res string
	return res, c.db.Get(&res, "select lang from meta where id=?", chatID)
}

func (c *sqliteClient) SetChatLanguage(chatID int64, lang string) error {
	query := `
		insert into "meta" (id, lang) values(:id, :lang)
		on conflict(id) do update set lang=excluded.lang;
	`
	return tool.Err(c.db.NamedExec(query, map[string]any{
		"id":   chatID,
		"lang": lang,
	}))
}

func (c *sqliteClient) UpsertChatMeta(chat *db.ChatMeta) error {
	query := `
		insert into chats (id, title, language, type, settings) values(:id, :title, :language, :type, :settings)
		on conflict(id) do update set title=excluded.title, language=excluded.language, type=excluded.type, settings=excluded.settings;
	`
	return tool.Err(c.db.NamedExec(query, chat))
}

func (c *sqliteClient) GetCharadeScore(chatID int64, userID int64) (*db.CharadeScore, error) {
	var res db.CharadeScore
	query := `
		select user_id, chat_id, score 
		from charade_scores 
		where user_id = cast(? as int) and chat_id=cast(? as bigint)`
	return &res, c.db.Get(&res, query, userID, chatID)
}

func (c *sqliteClient) GetCharadeStats(chatID int64) ([]*db.CharadeScore, error) {
	res := make([]*db.CharadeScore, 0)
	query := `
		select user_id, chat_id, score
		from charade_scores
		where chat_id=cast(? as bigint)`
	return res, c.db.Select(&res, query, chatID)
}

func (c *sqliteClient) AddCharadeScore(chatID int64, userID int64) (*db.CharadeScore, error) {
	if _, err := c.db.Exec(`
		insert into charade_scores (user_id, chat_id, score) values(?, ?, 1)
		on conflict(user_id, chat_id) do update set score=score+1;
	`, userID, chatID); err != nil {
		return nil, errors.WithMessage(err, "cant add charade score")
	}
	return c.GetCharadeScore(chatID, userID)
}
