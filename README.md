# :shield: Telegram Chat Gatekeeper bot
> Get rid of the unwanted spam joins out of the box

![Demo](https://user-images.githubusercontent.com/239034/142725561-5fd80514-dae9-4d29-aa19-a7d2ad41e362.png)

## Join protection
1. Triggered by new chat members and **join requests**.
2. Restricts the newcomer while verification is active.
3. Sends a CAPTCHA-style challenge with configurable option count and timeout.
4. On success, the newcomer is approved/unrestricted and the challenge message is cleaned up.
5. On failure or timeout, the newcomer is banned for the configured reject timeout.
6. Optional greeting text can be shown immediately with the public CAPTCHA for direct joins, or after approval for join-request newcomers.

## Spam protection
1. Every non-member's first message is checked through multiple fast paths:
 - **Known spammers DB lookup**
 - **Manual allowlist ("Indulgence") override**
 - **External quote heuristic** for obvious cross-chat spam patterns
 - **LLM-powered binary classification** with built-in and chat-specific spam examples
2. If the message is considered spam, the user is either immediately banned or sent into community voting, depending on chat settings.
3. If the message is clean, the user is remembered as a trusted member.

## Admin panel
1. Run `/settings` in a group where the bot is an admin.
2. The bot sends a deep-link that opens a private admin panel for that chat.
3. From there you can configure gatekeeper, LLM first-message moderation, community voting, spam examples, language, and manual not-spammer overrides.
4. The home screen includes a one-tap `Recommended Protection` preset and a compact 7-day protection summary.

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
7. Optional: Open `/settings` as a group admin and apply `Recommended Protection`.

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
| :heavy_check_mark: | `NG_LLM_API_KEY` | LLM provider API key for content analysis | | |
| | `NG_LANG` | Default language to use in new chats | `en` | `be`, `bg`, `cs`, `da`, `de`, `el`, `en`, `es`, `et`, `fi`, `fr`, `hu`, `id`, `it`, `ja`, `ko`, `lt`, `lv`, `nb`, `nl`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, `zh` |
| | `NG_HANDLERS` | Enabled bot handlers | `admin,gatekeeper,reactor` | Comma-separated list of handlers |
| | `NG_LOG_LEVEL` | Logging verbosity | `2` | `0`=Panic, `1`=Fatal, `2`=Error, `3`=Warn, `4`=Info, `5`=Debug, `6`=Trace |
| | `NG_DOT_PATH` | Bot data storage path | `~/.ngbot` | Any valid filesystem path |
| | `NG_LLM_API_MODEL` | LLM model to use | `gpt-4o-mini` | Any valid OpenAI or Gemini model |
| | `NG_LLM_API_URL` | OpenAI-compatible API base URL | `https://api.openai.com/v1` | Used when `NG_LLM_API_TYPE=openai` |
| | `NG_LLM_API_TYPE` | LLM provider | `openai` | `openai`, `gemini` |
| | `NG_FLAGGED_EMOJIS` | Emojis used for content flagging | `👎,💩` | Comma-separated list of emojis |
| | `NG_SPAM_LOG_CHANNEL_USERNAME` | Channel for spam logging | | Any valid channel username |
| | `NG_SPAM_VERBOSE` | Verbose in-chat notifications | `false` | `true`, `false` |
| | `NG_SPAM_VOTING_TIMEOUT` | Voting time limit | `5m` | Any valid duration string |
| | `NG_SPAM_MIN_VOTERS` | Minimum required voters | `2` | Any positive integer |
| | `NG_SPAM_MAX_VOTERS` | Maximum voters cap | `10` | Any positive integer |
| | `NG_SPAM_MIN_VOTERS_PERCENTAGE` | Minimum voter percentage | `5` | Any positive float |
| | `NG_SPAM_SUSPECT_NOTIFICATION_TIMEOUT` | Suspect notification timeout | `2m` | Any valid duration string |

## Troubleshooting
Don't hesitate to contact me

[![telegram](https://user-images.githubusercontent.com/239034/142726254-d3378dee-5b73-41b0-858d-b2a6e85dc735.png)
](https://t.me/WaveCut) [![linkedin](https://user-images.githubusercontent.com/239034/142726236-86c526e0-8fc3-4570-bd2d-fc7723d5dc09.png)
](https://linkedin.com/in/wavecut)

## Notes

- Gemini requests can reuse server-side explicit caching for the static moderation prefix when the provider supports it.
- Chat-specific settings, spam examples, and the private settings UI are already implemented.

Feel free to add feature requests in issues.
