module github.com/iamwavecut/ngbot

go 1.22.0

toolchain go1.23.2

require (
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/jmoiron/sqlx v1.4.0
	github.com/mitchellh/go-homedir v1.1.0
	github.com/nlpodyssey/cybertron v0.2.1
	github.com/pborman/uuid v1.2.1
	github.com/pkg/errors v0.9.1
	github.com/rubenv/sql-migrate v1.7.0
	github.com/sirupsen/logrus v1.9.3
	gopkg.in/yaml.v2 v2.4.0
	modernc.org/sqlite v1.33.1
)

require (
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/flatbuffers v24.3.25+incompatible // indirect
	github.com/google/pprof v0.0.0-20240424215950-a892ee059fd6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/nlpodyssey/gopickle v0.3.0 // indirect
	github.com/nlpodyssey/gotokenizers v0.2.0 // indirect
	github.com/nlpodyssey/spago v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/text v0.19.0 // indirect
	google.golang.org/protobuf v1.35.1 // indirect
	modernc.org/gc/v3 v3.0.0-20241004144649-1aea3fae8852 // indirect
	modernc.org/libc v1.61.0 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

require (
	github.com/go-gorp/gorp/v3 v3.1.0 // indirect
	github.com/iamwavecut/tool v1.2.6
	github.com/rs/zerolog v1.33.0
	github.com/sashabaranov/go-openai v1.32.3
	github.com/sethvargo/go-envconfig v1.1.0
	golang.org/x/exp v0.0.0-20241009180824-f66d83c29e7c // indirect
	golang.org/x/sys v0.26.0 // indirect
)

replace github.com/go-telegram-bot-api/telegram-bot-api/v5 => github.com/OvyFlash/telegram-bot-api/v5 v5.0.0-20240316083515-def9b6b5dc12
