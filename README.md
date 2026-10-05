# :shield: Telegram Chat Gatekeeper bot
> Get rid of the unwanted spam joins out of the box

![Demo](https://user-images.githubusercontent.com/239034/142725561-5fd80514-dae9-4d29-aa19-a7d2ad41e362.png)

## Join protection
1. Triggered by new chat members and **join requests**.
2. Applies the chat's manual allowlist before checking known spammer sources. Non-allowlisted known spammers are declined/banned immediately and join artifacts are cleaned up.
3. Restricts the newcomer while verification is active.
4. Sends a CAPTCHA-style challenge with configurable option count and timeout.
5. On success, the newcomer is approved/unrestricted and the challenge message is cleaned up.
6. On failure or timeout, the newcomer is banned for the configured reject timeout.
7. Optional greeting text can be shown immediately with the public CAPTCHA for direct joins, or after approval for join-request newcomers.

## Spam protection
1. Message authors are identified per chat as either a user or a sender channel. Channel identity takes precedence over Telegram's technical sender. Group membership and reaction history do not grant message trust.
2. By default, three distinct safe new texts, captions, or visible rich-message texts grant 30 days of trust. After expiry, one safe new message renews it. Commands, bot mentions, edits, reactions, and media without meaningful text never advance or renew trust. Safe checks and their message bindings are committed atomically; duplicates cannot advance the count twice.
3. Manual user allowlists precede user banlist enforcement. Automatic trust never bypasses banlists or reports. Linked-channel posts and anonymous group administrators keep their existing exemptions. Without moderation rights, no provider-backed checks or punitive actions run.
4. The classifier receives the direct reply/quote, source post, and up to five recent replies from the same thread. Context is limited to 24 hours, 2,000 characters per saved message, and 8,000 additional characters per request. Unknown discussion threads use only the available reply chain; ordinary groups can use recent group messages. Conversation is untrusted evidence; only the current message is classified. External replies have no automatic spam verdict.
5. Before admission and after expiry, semantic edits are checked. During trust, edits to previously checked messages remain protected. Trusted messages are retained for context, edits update it, and bot deletions remove it with replay protection. Telegram does not reliably notify bots when users delete ordinary messages.
6. Automatic spam suspicions follow the chat's community-voting setting. Suspect messages are deleted; users may be muted while voting, but channels have no mute step. Channels are banned only after confirmation, immediately when voting is disabled. Open cases suspend admission; confirmed spam resets trust and its counter, while false positives restore any still-valid prior expiry. Voters must be actual chat members; channel owners are not inferred.
7. Report missed spam with `/voteban` or by mentioning the bot in reply. Reports are checked independently of automatic trust. Confirmed user reports and administrator decisions keep their immediate moderation path; automatic channel suspicions follow community voting.

On migration, previously effective member trust and completed probations receive 30 days from migration time. Active probations restart with zero safe messages; reaction-only non-member records grant no trust. Existing cases, votes, message bindings and pending actions are preserved. Downgrade is refused while channel cases/bindings or pending CAPTCHA context cleanup exist, to avoid losing author identity or unfinished work.

The accepted tradeoff is that an attacker can earn trust with three harmless messages. Subsequent new messages are then exempt from automatic LLM checks until expiry, while reports, protected edits and user banlists remain active.

## Admin panel
1. Run `/settings` in a group where the bot is an admin.
2. The bot sends a deep-link that opens a private admin panel for that chat.
3. From there you can configure gatekeeper, message author trust, community voting, the LLM moderation profile, allowed/spam examples, language, and manual not-spammer overrides.
4. The home screen includes a one-tap `Recommended Protection` preset and a compact 7-day protection summary.

## Installation

### Quick Start with Docker Compose
1. Create a bot via [BotFather](https://t.me/BotFather) and enable group messages access.
2. Clone the repository:
```bash
git clone https://github.com/iamwavecut/ngbot.git
cd ngbot
```
3. Create a mode-`0600` environment file and configure it. `compose.yaml` is the tracked deployment source; do not copy or maintain a second Compose file:
```bash
install -m 0600 /dev/null .env
sed '/^[[:space:]]*#/d; /^[[:space:]]*$/d' .env.example > .env
chmod 0600 .env
# Edit .env and set NG_TOKEN, NGBOT_DATA_PATH, and the selected LLM credential.
```
Never commit `.env`, paste it into logs, or pass it on a command line. See [Operator deployment guide](deploy/README.md) for secret rotation and release identity checks.
4. Create the writable data directory named by `NGBOT_DATA_PATH` and give the distroless `nonroot` user ownership:
```bash
sudo install -d -m 0700 -o 65532 -g 65532 /home/username/.ngbot
```
The image runs as UID/GID `65532:65532`; set `NGBOT_DATA_PATH=/home/username/.ngbot`. Compose always mounts that host path at `/data`, and the application always uses `NG_DOT_PATH=/data` in the container.
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
1. Create the bot and a mode-`0600` `.env` as above.
2. Export the variables before starting the native binary. The Go binary does **not** load `.env` by itself:
```bash
set -a
. ./.env
set +a
go mod download
go run ./cmd/ngbot
```
The native Mini App server defaults to loopback at `127.0.0.1:8080`. Only the Compose deployment overrides it to `0.0.0.0:8080` inside the isolated container; Docker publishes it exclusively on host loopback.

## Configuration
All configuration is done through environment variables. You can:
- Set them in your environment
- Let Docker Compose read a mode-`0600` `.env` file
- Source a mode-`0600` `.env` before running the native binary

See [.env.example](.env.example) for a quick reference. `NGBOT_*` variables configure only the Compose/build workflow; `NG_*` variables configure the application.

| Compose/build variable | Purpose | Default |
| --- | --- | --- |
| `NGBOT_DATA_PATH` | Secured host directory mounted at `/data` | Required |
| `NGBOT_WEBAPP_HOST_PORT` | WebApp port published on host loopback | `18080` |
| `NGBOT_VERSION` | Release version stored in the binary and OCI label | `dev` |
| `NGBOT_REVISION` | Exact Git revision stored in the binary and OCI label | `unknown` |
| `NGBOT_BUILD_DATE` | RFC 3339 build time stored in the binary and OCI label | `unknown` |

### Configuration Options

| Required | Variable name | Description | Default | Options |
| --- | --- | --- | --- | --- |
| :heavy_check_mark: | `NG_TOKEN` | Telegram BOT API token | | |
| | `NG_LANG` | Default language to use in new chats | `en` | `be`, `bg`, `cs`, `da`, `de`, `el`, `en`, `es`, `et`, `fi`, `fr`, `hu`, `id`, `it`, `ja`, `ko`, `lt`, `lv`, `nb`, `nl`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, `zh` |
| | `NG_HANDLERS` | Enabled bot handlers | `admin,gatekeeper,reactor` | Comma-separated list of handlers |
| | `NG_LOG_LEVEL` | Logging verbosity | `2` | `0`=Panic, `1`=Fatal, `2`=Error, `3`=Warn, `4`=Info, `5`=Debug, `6`=Trace |
| | `NG_DOT_PATH` | Bot data storage path | `~/.ngbot` | Any valid filesystem path |
| | `NG_TELEGRAM_POLL_TIMEOUT` | Telegram long poll timeout | `60s` | Any valid duration string |
| | `NG_TELEGRAM_REQUEST_TIMEOUT` | Telegram HTTP request timeout | `75s` | Must be greater than poll timeout |
| | `NG_TELEGRAM_RECOVERY_WINDOW` | Maximum degraded polling window before restart | `10m` | Must be greater than request timeout |
| | `NG_TELEGRAM_INBOX_MAX_PENDING_ROWS` | Maximum aggregate pending/retry inbox rows | `100000` | Positive integer |
| | `NG_TELEGRAM_INBOX_MAX_PENDING_BYTES` | Maximum aggregate pending/retry payload bytes | `536870912` | Positive integer |
| | `NG_TELEGRAM_INBOX_MAX_DISPATCH_PENDING_ROWS` | Maximum pending/retry rows for one dispatch key | `10000` | Positive integer |
| | `NG_TELEGRAM_INBOX_MAX_DISPATCH_PENDING_BYTES` | Maximum pending/retry bytes for one dispatch key | `33554432` | Positive integer |
| | `NG_TELEGRAM_INBOX_MIN_FREE_BYTES` | Minimum database filesystem free space for admission | `268435456` | Positive integer |
| | `NG_GATEKEEPER_WEBAPP_PUBLIC_URL` | Public HTTPS origin for join-request CAPTCHA Mini App | | Absolute URL, e.g. `https://captcha.example.com` |
| | `NG_GATEKEEPER_WEBAPP_LISTEN_ADDR` | Native embedded Mini App server listen address | `127.0.0.1:8080` | Compose enforces `0.0.0.0:8080` inside the container |
| | `NG_GATEKEEPER_WEBAPP_MAX_CONCURRENT` | Maximum in-flight Mini App requests | `32` | Integer greater than zero; `0` is invalid |
| | `NG_GATEKEEPER_WEBAPP_REQUESTS_PER_MINUTE` | Per-client Mini App request limit | `120` | Integer greater than zero; `0` is invalid |
| | `NG_LLM_GEMINI_API_KEY` | Gemini credential; required when `reactor` uses Gemini | | Preferred over the legacy key |
| | `NG_LLM_OPENROUTER_API_KEY` | OpenRouter credential; required when using OpenRouter | | Preferred over the legacy key |
| | `NG_LLM_OPENAI_API_KEY` | OpenAI credential; required when `reactor` uses OpenAI | | Preferred over the legacy key |
| | `NG_LLM_API_KEY` | Legacy credential fallback for the selected provider | | Used only when its dedicated key is empty |
| | `NG_LLM_API_MODEL` | Optional LLM model override | Provider-specific | OpenAI/Gemini overrides; OpenRouter requires `deepseek/deepseek-v4.1-flash` |
| | `NG_LLM_API_URL` | OpenAI-compatible API base URL | `https://api.openai.com/v1` | Used when `NG_LLM_API_TYPE=openai` |
| | `NG_LLM_API_TYPE` | LLM provider | `openai` | `openai`, `gemini`, `openrouter` |
| | `NG_LLM_REQUEST_TIMEOUT` | Maximum duration of one classification request | `45s` | Any positive duration string |
| | `NG_SPAM_LOG_CHANNEL_USERNAME` | Channel for spam logging | | Any valid channel username |
| | `NG_SPAM_DEBUG_USER_ID` | User allowed to run diagnostics in private chat | `0` | Telegram user ID |
| | `NG_SPAM_VERBOSE` | Verbose in-chat notifications | `false` | `true`, `false` |
| | `NG_SPAM_SAFE_MESSAGES_REQUIRED` | Distinct safe new messages for initial admission | `3` | Positive integer |
| | `NG_SPAM_AUTHOR_TRUST_DURATION` | Author trust lifetime and renewal period | `720h` | Positive duration |
| | `NG_SPAM_MESSAGE_PROBATION_DURATION` | Deprecated, accepted with a warning and ignored | Empty | Legacy duration string |
| | `NG_SPAM_VOTING_TIMEOUT` | Voting time limit | `5m` | Any valid duration string |
| | `NG_SPAM_MIN_VOTERS` | Minimum required voters | `2` | Any positive integer |
| | `NG_SPAM_MAX_VOTERS` | Maximum voters cap | `10` | Any positive integer |
| | `NG_SPAM_MIN_VOTERS_PERCENTAGE` | Minimum voter percentage | `5` | Any positive float |
| | `NG_SPAM_SUSPECT_NOTIFICATION_TIMEOUT` | Suspect notification timeout | `2m` | Any valid duration string |

The language codes in `NG_LANG` are the same complete locale catalog used by the admin UI and CAPTCHA resources. CI verifies that translation keys remain complete across that catalog.

### Caddy reverse proxy

The Docker Compose file binds the Mini App server to `127.0.0.1:${NGBOT_WEBAPP_HOST_PORT:-18080}` on the host. A matching Caddy template is available at `deploy/caddy/ngbot-webapp.Caddyfile`.

Put the application values in the same mode-`0600` `.env` that Compose reads automatically:

```bash
NG_GATEKEEPER_WEBAPP_PUBLIC_URL=https://antifraud.rtfm.rsvp
NGBOT_WEBAPP_HOST_PORT=18080
```

These are file entries, not shell assignments to run before `docker compose`: Compose reads `.env` itself. It enforces the container listener, so leave `NG_GATEKEEPER_WEBAPP_LISTEN_ADDR` at its native default in `.env`.

Configure the separate Caddy service environment with the public domain and the same canonical host-port variable (for example through its systemd `Environment=` or `EnvironmentFile=` settings):

```bash
NGBOT_GATEKEEPER_WEBAPP_DOMAIN=antifraud.rtfm.rsvp
NGBOT_WEBAPP_HOST_PORT=18080
```

Do not give Caddy the bot `.env`, because it contains application credentials that Caddy does not need.

The Mini App endpoint is intentionally hostile to indexing and unauthorized embedding:

1. `/robots.txt` disallows all crawlers, including known search, SEO, and LLM training bots.
2. `/sitemap.xml` is an empty sitemap.
3. `X-Robots-Tag` denies indexing, following, snippets, archives, image indexing, translation, AI use, and image-AI use.
4. CSP uses `default-src 'none'` and per-request nonces for the Telegram script and local inline code.
5. CSP allows framing only by the official Telegram Web origin; attacker origins remain blocked.
6. Browser capability APIs are disabled with `Permissions-Policy`.
7. Referrers are suppressed with `Referrer-Policy: no-referrer`.
8. Responses are marked `no-store` and `private`.
9. Cross-origin resource sharing is not enabled, and cross-site mutation requests are rejected through Fetch Metadata.
10. POST bodies are size-limited before form parsing.
11. Known crawler and LLM user agents are rejected before challenge lookup.
12. MIME sniffing and legacy cross-domain policies are disabled.
13. Concurrent admission and per-client request rates are bounded; access telemetry records only method, an allowlisted route name, status, and duration, never raw paths, query strings, authorization headers, or bearer tokens.

The embedded server exposes `GET /livez` for WebApp process liveness and `GET /readyz` for application readiness. Readiness is published only after every lifecycle component, including the durable update loop, starts successfully and is withdrawn before shutdown. A fatal serving error triggers graceful process shutdown with exit status 1 so Compose can restart the service. Container health checks use `/readyz`; use `./ngbot --version`, the OCI revision label, and the running container image ID to verify a deployed artifact. See [deploy/README.md](deploy/README.md) for the executable release and rollback procedure.

### Production SQLite maintenance

Do not run `PRAGMA quick_check`, `integrity_check`, or other long scans against the live database file. Create a consistent online snapshot first, then run integrity and migration checks against the snapshot:

```bash
umask 077
sqlite3 /home/username/.ngbot/bot.db ".backup '/var/backups/ngbot/bot-audit.db'"
chmod 0600 /var/backups/ngbot/bot-audit.db
test "$(sqlite3 /var/backups/ngbot/bot-audit.db 'PRAGMA quick_check;')" = ok
test -z "$(sqlite3 /var/backups/ngbot/bot-audit.db 'PRAGMA foreign_key_check;')"
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
- Chat-specific settings, moderation profiles, labeled examples, and the private settings UI are already implemented.

## Acknowledgements

This bot benefits from public anti-spam data shared with the community by:

- [Combot Anti-Spam (CAS)](https://cas.chat/) for the CAS spammer database and API.
- [LoLs bot](https://lols.bot/) for spammer lists and account checks.

Thank you to both projects for maintaining and sharing these community safety resources.

Feel free to add feature requests in issues.

### Author moderation diagnostics

Structured debug logs include `author_kind`, `author_id`, chat/message IDs, `trust_phase`, skip reason and classification outcome without message text. Existing provider usage logs retain token counts. Daily SQLite KV statistics use `stats:<chat_id>:<YYYY-MM-DD>:<metric>` keys: `author_check_initial`, `author_check_renewal`, `author_check_edit`, `author_check_pending_case`, `author_check_report`, `author_trust_granted`, and `author_trust_skipped`. Check counters include attempts; `llm_checked` counts successful message/report classifications, and existing `spam_confirmed` / `false_positive` counters record resolved cases. Compare equivalent traffic windows after rollout; local tests do not establish the cause or savings of a production incident.

Context cleanup runs through the existing periodic bounded retention job. Checked-message bindings contain only identity and timing metadata, and remain until the chat is removed. They survive trust expiry, renewal, resets and context deletion so that an old checked message cannot lose edit protection during a later trust period.

## Public comment classification and OpenRouter accounting

The initial check and reported-message check share one policy. Public-channel subscribers do not have to join the discussion group. Spam requires promotional/referral redirection or an invitation to unspecified income/work. News sources, documentation, ordinary social profiles, detailed vacancies (including iGaming recruitment), contextual recommendations, criticism and jokes remain allowed. Telegram bot `start`/`startapp` payloads require scrutiny: unexplained personal or opaque referral-like codes are spam, while clearly contextual document/function links and technical examples are allowed. Quoting a scam to discuss or warn about it is allowed. Existing author trust, banlists, CAPTCHA, voting and per-chat settings remain separate.

Select `NG_LLM_API_TYPE=openrouter`, set `NG_LLM_OPENROUTER_API_KEY`, and leave the model empty or set `deepseek/deepseek-v4.1-flash`. Each request permits only the official `deepseek` provider, disables provider fallbacks and requires parameter support. The OpenRouter endpoint is fixed to `https://openrouter.ai/api/v1`; the OpenAI base URL is unrelated. Requests use low reasoning effort with a 2,048-token completion budget, including reasoning. Truncated, refused, malformed or unexpected-provider responses cannot become spam verdicts. DeepSeek implicit prefix caching needs no cache-management calls. Stable policy and global examples precede changing context and candidate data.

At log level `4` (info) or higher, `OpenRouter usage metadata` records normalized/native token counts, cached tokens, cache writes when reported, reasoning, cost and cost breakdowns, generation/request/upstream IDs, actual provider, finish reasons and timings. Numeric usage fields are preserved even when the SDK has no typed field. A background queue of at most 32 metadata lookups retries `/generation` for up to ten minutes, because OpenRouter publishes these records after a delay. Separate generation records join the initial usage record by generation ID; shutdown cancels pending lookups. Unavailable statistics do not invalidate a valid verdict. No prompt, completion text, reasoning text, credentials or external-user identity is logged. Missing cache/cost observations are distinguished from measured zeros. Cumulative ratios cover completed requests since process start; raw records remain subject to Compose log rotation (five 10 MiB files).

Aggregate any retained time window without adding costs from usage and generation twice:

```sh
docker compose logs --no-log-prefix --since 24h ngbot | ./scripts/openrouter-stats.py
```

`cache_hit_request_rate` measures requests with any reported hit among cache-observed requests; `cached_input_token_rate` measures cached input tokens among cache-observed input tokens. Reasoning tokens are part of completion tokens and must not be added again. Reported cache discounts and actual billed costs are separate; no assumed list-price savings are substituted for observed billing. Generation IDs deduplicate repeated log records. Export metadata logs before rotation for longer reporting windows.

The opt-in semantic regression uses the real pinned provider and incurs API charges:

```sh
NGBOT_RUN_LIVE_OPENROUTER_MODERATION=1 go test ./internal/handlers/moderation -run '^TestLiveOpenRouterPublicComments$' -count=1 -v
```
