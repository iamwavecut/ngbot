module github.com/iamwavecut/ngbot

go 1.16

require (
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/jmoiron/sqlx v1.3.5
	github.com/mattn/go-sqlite3 v2.0.3+incompatible
	github.com/mitchellh/go-homedir v1.1.0
	github.com/pborman/uuid v1.2.1
	github.com/pkg/errors v0.9.1
	github.com/rubenv/sql-migrate v1.3.1
	github.com/sirupsen/logrus v1.9.0
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/go-gorp/gorp/v3 v3.1.0 // indirect
	github.com/iamwavecut/tool v1.0.1 // indirect
	github.com/sethvargo/go-envconfig v0.9.0
	golang.org/x/exp v0.0.0-20230213192124-5e25df0256eb // indirect
	golang.org/x/sys v0.5.0 // indirect
)

replace github.com/go-telegram-bot-api/telegram-bot-api/v5 => github.com/iamwavecut/telegram-bot-api/v6 v6.3.2
