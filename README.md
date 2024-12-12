# :shield: Telegram Chat Gatekeeper bot
> Get rid of the unwanted spam joins out of the box

![Demo](https://user-images.githubusercontent.com/239034/142725561-5fd80514-dae9-4d29-aa19-a7d2ad41e362.png)

## Join protection
0. Join-time challenge is disabled as for now, due to being buggy, but will be back as option in the future.
> 1. Triggered on the events, which introduces new chat members (invite, join, etc). Also works with **join requests**.
> 2. Restrict newcomer to be read-only.
> 3. Set up a challenge for the newcomer (join request), which is a simple task as shown on the image above, but yet, unsolvable for the vast majority of automated spam robots.
> 4. If the newcomer succeeds in choosing the right answer - restrictions gets fully lifted, challenge ends.
> 5. Otherwise - newcomer gets banned for 10 minutes (There is a "false-positive" chance, rememeber? Most robots aint coming back, anyway).
> 6. If the newcomer struggles to answer in a set period of time (defaults to 3 minutes) - challenge automatically fails the same way, as in p.5.
> 7. After the challenge bot cleans up all related messages, only leaving join notification for the newcomers, that made it. There are no traces of unsuccesful joins left, and that is awesome.

## Spam protection
1. Every chat member first message is being checked for spam using two approaches:
 - **Known spammers DB lookup** - checks if the message author is in the known spammers DB.
 - **GPT-powered content analysis** - asks GPT to analyze the message for harmful content.
2. If the message is considered as spam - newcomer gets kick-banned.
3. If the message is not considered as spam - user becomes a normal trusted chat member.

## Installation

### Quick Start with Docker Compose
1. Create a bot via [BotFather](https://t.me/BotFather) and enable group messages access.
2. Clone the repository:
```bash
git clone https://github.com/iamwavecut/ngbot.git
cd ngbot
```
3. Copy the example environment file and configure it:
```bash
cp .env.example .env
# Edit .env with your favorite editor and set required variables
```
4. Start the bot:
```bash
docker compose up -d
```
5. Add your bot to chat and give it **Ban**, **Delete**, and **Invite** permissions.
6. Optional: Change bot language with `/lang <code>` (e.g., `/lang ru`).

### Manual Installation
1. Follow steps 1-3 from Quick Start.
2. Build and run:
```bash
go mod download
go run .
```

## Configuration
All configuration is done through environment variables. You can:
- Set them in your environment
- Use a `.env` file (recommended)
- Pass them directly to docker compose or the binary

See [.env.example](.env.example) for a quick reference of all available options.

### Configuration Options

| Required | Variable name | Description | Default | Options |
| --- | --- | --- | --- | --- |
| :heavy_check_mark: | `NG_TOKEN` | Telegram BOT API token | | |
| :heavy_check_mark: | `NG_OPENAI_API_KEY` | OpenAI API key for content analysis | | |
| :x: | `NG_LANG` | Default language to use in new chats | `en` | `be`, `bg`, `cs`, `da`, `de`, `el`, `en`, `es`, `et`, `fi`, `fr`, `hu`, `id`, `it`, `ja`, `ko`, `lt`, `lv`, `nb`, `nl`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, `zh` |
| :x: | `NG_HANDLERS` | Enabled bot handlers | `admin,gatekeeper,reactor` | Comma-separated list of handlers |
| :x: | `NG_LOG_LEVEL` | Logging verbosity | `2` | `0`=Panic, `1`=Fatal, `2`=Error, `3`=Warn, `4`=Info, `5`=Debug, `6`=Trace |
| :x: | `NG_DOT_PATH` | Bot data storage path | `~/.ngbot` | Any valid filesystem path |
| :x: | `NG_OPENAI_MODEL` | OpenAI model to use | `gpt-4o-mini` | Any valid OpenAI model |
| :x: | `NG_OPENAI_BASE_URL` | OpenAI API base URL | `https://api.openai.com/v1` | Any valid OpenAI API compliant base URL |
| :x: | `NG_FLAGGED_EMOJIS` | Emojis used for content flagging | `👎,💩` | Comma-separated list of emojis |
| :x: | `NG_SPAM_LOG_CHANNEL_USERNAME` | Channel for spam logging | | Any valid channel username |
| :x: | `NG_SPAM_VERBOSE` | Verbose in-chat notifications | `false` | `true`, `false` |
| :x: | `NG_SPAM_VOTING_TIMEOUT` | Voting time limit | `5m` | Any valid duration string |
| :x: | `NG_SPAM_MIN_VOTERS` | Minimum required voters | `2` | Any positive integer |
| :x: | `NG_SPAM_MAX_VOTERS` | Maximum voters cap | `10` | Any positive integer |
| :x: | `NG_SPAM_MIN_VOTERS_PERCENTAGE` | Minimum voter percentage | `5` | Any positive float |
| :x: | `NG_SPAM_SUSPECT_NOTIFICATION_TIMEOUT` | Suspect notification timeout | `2m` | Any valid duration string |

## Troubleshooting
Don't hesitate to contact me

[![telegram](https://user-images.githubusercontent.com/239034/142726254-d3378dee-5b73-41b0-858d-b2a6e85dc735.png)
](https://t.me/WaveCut) [![linkedin](https://user-images.githubusercontent.com/239034/142726236-86c526e0-8fc3-4570-bd2d-fc7723d5dc09.png)
](https://linkedin.com/in/wavecut)

## TODO

- [ ] Individual chat's settings (behaviours, timeouts, custom welcome messages, etc).
- [ ] Chat-specific spam filters.
- [ ] Settings UI in private and/or web.

> Feel free to add your requests in issues.
