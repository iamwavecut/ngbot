module github.com/iamwavecut/ngbot

go 1.18

require (
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/jmoiron/sqlx v1.3.5
	github.com/mattn/go-sqlite3 v1.14.17
	github.com/mitchellh/go-homedir v1.1.0
	github.com/pborman/uuid v1.2.1
	github.com/pkg/errors v0.9.1
	github.com/rubenv/sql-migrate v1.5.2
	github.com/sirupsen/logrus v1.9.3
	gopkg.in/yaml.v2 v2.4.0
	modernc.org/sqlite v1.25.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.3.1 // indirect
	github.com/iamwavecut/telegram-bot-api v0.0.0-20230916213553-9a80e5e4153f // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/mod v0.12.0 // indirect
	golang.org/x/tools v0.13.0 // indirect
	lukechampine.com/uint128 v1.3.0 // indirect
	modernc.org/cc/v3 v3.41.0 // indirect
	modernc.org/ccgo/v3 v3.16.15 // indirect
	modernc.org/libc v1.24.1 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.7.1 // indirect
	modernc.org/opt v0.1.3 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

require (
	github.com/go-gorp/gorp/v3 v3.1.0 // indirect
	github.com/iamwavecut/tool v1.2.2
	github.com/sethvargo/go-envconfig v0.9.0
	golang.org/x/exp v0.0.0-20230905200255-921286631fa9 // indirect
	golang.org/x/sys v0.12.0 // indirect
)

replace github.com/go-telegram-bot-api/telegram-bot-api/v5 => github.com/iamwavecut/telegram-bot-api v0.0.0-20230218213054-8b84e43be657
