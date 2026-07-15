# :shield: Telegram Chat Gatekeeper bot
> Get rid of the unwanted spam joins out of the box

![Demo](https://user-images.githubusercontent.com/239034/142725561-5fd80514-dae9-4d29-aa19-a7d2ad41e362.png)

## Join protection
1. Triggered by new chat members and **join requests**.
2. Checks known spammer sources before CAPTCHA or greeting. Known spammers are declined/banned immediately and join artifacts are cleaned up.
3. Restricts the newcomer while verification is active.
4. Sends a CAPTCHA-style challenge with configurable option count and timeout.
5. On success, the newcomer is approved/unrestricted and the challenge message is cleaned up.
6. On failure or timeout, the newcomer is banned for the configured reject timeout.
7. Optional greeting text can be shown immediately with the public CAPTCHA for direct joins, or after approval for join-request newcomers.

## Spam protection
1. Every non-member's first message is checked through multiple fast paths:
   - **Manual allowlist ("Indulgence") override**
   - **Known spammers lookup** from local imports and online checks against LoLs bot and CAS/Combot
   - **External quote heuristic** for obvious cross-chat spam patterns
   - **LLM-powered binary classification** with built-in and chat-specific spam examples
2. If the message is considered spam, the user is either immediately banned or sent into community voting, depending on chat settings.
3. Chat users can report missed spam with `/voteban` or by mentioning the bot in reply to the message. Reports are rechecked by the LLM first, then either moderated immediately or sent to community voting without pre-deleting the original message.
4. If the message is clean, the user is remembered so repeat checks can be reduced where possible.

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
cp compose.yaml.dist compose.yaml
cp .env.example .env
# Edit .env with your favorite editor and set required variables
```
4. Create the writable data directory from `compose.yaml` and give the distroless `nonroot` user ownership:
```bash
sudo install -d -m 0700 -o 65532 -g 65532 /home/username/.ngbot
```
The image runs as UID/GID `65532:65532`; update the bind-mount `device` path in `compose.yaml` before starting it.
5. Start the bot:
```bash
docker compose up -d
```
6. Optional for join-request Mini App CAPTCHA: set `NG_GATEKEEPER_WEBAPP_PUBLIC_URL=https://antifraud.rtfm.rsvp`, point Caddy at `deploy/caddy/ngbot-webapp.Caddyfile`, then restart Compose.
7. Add your bot to chat and give it **Ban**, **Delete**, and **Invite** permissions.
8. Optional: Change bot language with `/lang <code>` (e.g., `/lang ru`).
9. Optional: Set `NG_SPAM_DEBUG_USER_ID` to your Telegram user ID for private `/testspam` and `/skipreason`; source-chat administrators may use those diagnostics in their chat.
10. Optional: Open `/settings` as a group admin and apply `Recommended Protection`.

### Manual Installation
1. Follow steps 1-3 from Quick Start.
2. Build and run:
```bash
go mod download
go run ./cmd/ngbot
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
| | `NG_TELEGRAM_POLL_TIMEOUT` | Telegram long poll timeout | `60s` | Any valid duration string |
| | `NG_TELEGRAM_REQUEST_TIMEOUT` | Telegram HTTP request timeout | `75s` | Must be greater than poll timeout |
| | `NG_TELEGRAM_RECOVERY_WINDOW` | Maximum degraded polling window before restart | `10m` | Must be greater than request timeout |
| | `NG_GATEKEEPER_WEBAPP_PUBLIC_URL` | Public HTTPS origin for join-request CAPTCHA Mini App | | Absolute URL, e.g. `https://captcha.example.com` |
| | `NG_GATEKEEPER_WEBAPP_LISTEN_ADDR` | Embedded Mini App server listen address inside the container | `:8080` | Keep `:8080` with the default Compose port mapping |
| | `NG_GATEKEEPER_WEBAPP_HOST_PORT` | Compose-only localhost port for Caddy reverse proxy | `18080` | Host port bound to `127.0.0.1` |
| | `NG_LLM_API_MODEL` | Optional LLM model override | Provider-specific | Any valid OpenAI or Gemini model |
| | `NG_LLM_API_URL` | OpenAI-compatible API base URL | `https://api.openai.com/v1` | Used when `NG_LLM_API_TYPE=openai` |
| | `NG_LLM_API_TYPE` | LLM provider | `openai` | `openai`, `gemini` |
| | `NG_LLM_REQUEST_TIMEOUT` | Maximum duration of one classification request | `45s` | Any positive duration string |
| | `NG_SPAM_LOG_CHANNEL_USERNAME` | Channel for spam logging | | Any valid channel username |
| | `NG_SPAM_DEBUG_USER_ID` | User allowed to run diagnostics in private chat | `0` | Telegram user ID |
| | `NG_SPAM_VERBOSE` | Verbose in-chat notifications | `false` | `true`, `false` |
| | `NG_SPAM_VOTING_TIMEOUT` | Voting time limit | `5m` | Any valid duration string |
| | `NG_SPAM_MIN_VOTERS` | Minimum required voters | `2` | Any positive integer |
| | `NG_SPAM_MAX_VOTERS` | Maximum voters cap | `10` | Any positive integer |
| | `NG_SPAM_MIN_VOTERS_PERCENTAGE` | Minimum voter percentage | `5` | Any positive float |
| | `NG_SPAM_SUSPECT_NOTIFICATION_TIMEOUT` | Suspect notification timeout | `2m` | Any valid duration string |

### Caddy reverse proxy

The Docker Compose file binds the Mini App server to `127.0.0.1:${NG_GATEKEEPER_WEBAPP_HOST_PORT:-18080}` on the host. A matching Caddy template is available at `deploy/caddy/ngbot-webapp.Caddyfile`.

Set these values on the host before enabling the Caddy site:

```bash
export NGBOT_GATEKEEPER_WEBAPP_DOMAIN=antifraud.rtfm.rsvp
export NG_GATEKEEPER_WEBAPP_HOST_PORT=18080
```

Then configure the bot with:

```bash
NG_GATEKEEPER_WEBAPP_PUBLIC_URL=https://antifraud.rtfm.rsvp
NG_GATEKEEPER_WEBAPP_LISTEN_ADDR=:8080
```

The Mini App endpoint is intentionally hostile to indexing and embedding:

1. `/robots.txt` disallows all crawlers, including known search, SEO, and LLM training bots.
2. `/sitemap.xml` is an empty sitemap.
3. `X-Robots-Tag` denies indexing, following, snippets, archives, image indexing, translation, AI use, and image-AI use.
4. CSP uses `default-src 'none'` and per-request nonces for the Telegram script and local inline code.
5. CSP and `X-Frame-Options` deny framing.
6. Browser capability APIs are disabled with `Permissions-Policy`.
7. Referrers are suppressed with `Referrer-Policy: no-referrer`.
8. Responses are marked `no-store` and `private`.
9. Cross-origin resource sharing is not enabled, and cross-site mutation requests are rejected through Fetch Metadata.
10. POST bodies are size-limited before form parsing.
11. Known crawler and LLM user agents are rejected before challenge lookup.
12. MIME sniffing and legacy cross-domain policies are disabled.

### Production SQLite maintenance

Do not run `PRAGMA quick_check`, `integrity_check`, or other long scans against the live database file. Create a consistent online snapshot first, then run integrity and migration checks against the snapshot:

```bash
sqlite3 /home/username/.ngbot/bot.db ".backup '/home/username/.ngbot/bot-audit.db'"
sqlite3 /home/username/.ngbot/bot-audit.db "PRAGMA quick_check; PRAGMA foreign_key_check;"
```

Delete the audit snapshot after verification. A direct long-running read can hold a SQLite shared lock long enough for application writes to reach the configured busy timeout.

The application has an offline maintenance mode that applies pending migrations, performs a full `VACUUM`, enables incremental auto-vacuum, runs `PRAGMA optimize`, validates the database, restores WAL mode, and exits without starting Telegram polling:

```bash
docker compose stop ngbot
docker compose run --rm --no-deps ngbot --database-maintenance
docker compose up -d ngbot
```

Create and verify a database backup before running this command. The service must remain stopped for the complete maintenance run. Normal banlist refreshes use inactive generations, short batched writes, atomic activation, bounded garbage collection, passive WAL checkpoints, and incremental vacuum, so full maintenance is not required after each refresh.

## Troubleshooting
Don't hesitate to contact me

[![telegram](https://user-images.githubusercontent.com/239034/142726254-d3378dee-5b73-41b0-858d-b2a6e85dc735.png)
](https://t.me/WaveCut) [![linkedin](https://user-images.githubusercontent.com/239034/142726236-86c526e0-8fc3-4570-bd2d-fc7723d5dc09.png)
](https://linkedin.com/in/wavecut)

## Notes

- Gemini requests can reuse server-side explicit caching for the static moderation prefix when the provider supports it.
- Chat-specific settings, spam examples, and the private settings UI are already implemented.

## Acknowledgements

This bot benefits from public anti-spam data shared with the community by:

- [Combot Anti-Spam (CAS)](https://cas.chat/) for the CAS spammer database and API.
- [LoLs bot](https://lols.bot/) for spammer lists and account checks.

Thank you to both projects for maintaining and sharing these community safety resources.

Feel free to add feature requests in issues.
