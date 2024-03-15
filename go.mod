module github.com/iamwavecut/ngbot

go 1.22

toolchain go1.22.1

require (
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/jmoiron/sqlx v1.3.5
	github.com/mitchellh/go-homedir v1.1.0
	github.com/pborman/uuid v1.2.1
	github.com/pkg/errors v0.9.1
	github.com/rubenv/sql-migrate v1.6.1
	github.com/sirupsen/logrus v1.9.3
	gopkg.in/yaml.v2 v2.4.0
	modernc.org/sqlite v1.29.5
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/gc/v3 v3.0.0-20240304020402-f0dba7c97c2b // indirect
	modernc.org/libc v1.44.1 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.7.2 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

require (
	github.com/go-gorp/gorp/v3 v3.1.0 // indirect
	github.com/iamwavecut/tool v1.2.2
	github.com/sethvargo/go-envconfig v1.0.1
	golang.org/x/exp v0.0.0-20240314144324-c7f7c6466f7f // indirect
	golang.org/x/sys v0.18.0 // indirect
)

replace github.com/go-telegram-bot-api/telegram-bot-api/v5 => github.com/OvyFlash/telegram-bot-api/v5 v5.0.0-20240108230938-63e5c59035bf
