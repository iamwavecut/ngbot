# Banlist snapshot storage design

## Why

On 2026-10-26 production ngbot wrote about 64 GB per banlist refresh and refreshed every hour. The CAS export now returns 404; `fetchKnownBannedFeed` advances the shared daily timestamp only when every provider succeeds, so the full 4.84 M-ID lols list was re-applied on every hourly tick and on every restart. Each apply inserted millions of rows in random order into two B-trees in 1,000-row autocommit batches, took 21-26 minutes, wrote 58.6 MB/s (95 % of host disk writes) and raised host iowait to 12-20 %, slowing every service on the shared disk. Evidence: Manifold `ngbot-prod-health-2026-10-26`.

All runtime lookups already use an in-memory set (`IsKnownBanned`). SQLite only persists the set for warm starts. The banlist therefore does not need millions of rows; it needs a durable copy of each external list that is cheap to replace.

## Goals

- Disk writes proportional to real change: zero when a list is unchanged, about the encoded list size (about 10 MB) when it changes.
- Every external source is fetched, scheduled and fails independently; one dead provider never forces another provider to re-import.
- A restart never triggers a re-import of a list checked within its interval.
- Database writes hold the write lock for one short transaction.
- CAS works again through its authenticated `check` method.
- The bot owner can manage global exceptions from Telegram.
- A second process polling with the same token no longer causes a restart loop.

## Non-goals

- No procedure to degrade or reconcile bans when an upstream provider unbans someone. The owner decided to handle such cases individually with exceptions.
- No new ban providers, no change to LLM moderation, voting, author trust or message context.
- No change to `moderation_action_fences` retention (about 274 MB with indexes, separate topic).

## Anti-goals

- A truncated or garbage download must never wipe or shrink the effective banlist.
- No secret (bot token, CAS API key) in logs, URLs, error messages or tests.
- No long write transactions that stall Telegram update processing.
- No reintroduction of full per-hour rewrites through another path (online positives, maintenance, migration).

## Decided by the owner

- Exceptions stay a separate small table and are checked before the banlist, per chat first and then global. They are never merged into ban sources (a per-chat exception must not unban globally).
- Only the bot owner can add, list and delete global exceptions.
- The frozen CAS export snapshot (1.25 M IDs, about 1.02 M not present in lols, last updated 2026-10-02) is kept as a read-only source. Unbans are handled individually.
- CAS is used through `GET https://api.cas.chat/check?user_id=<id>` with the Combot API key.

## Storage

Two new tables replace `banlist`, `banlist_entries` and `banlist_generations`.

`banlist_snapshots`, one row per bulk source:

| Column | Meaning |
|---|---|
| `source TEXT PRIMARY KEY` | `lols/banlist`, `lols/scammers`, `cas/legacy` |
| `format INTEGER NOT NULL` | encoding version, `1` |
| `id_count INTEGER NOT NULL` | number of IDs |
| `payload BLOB NOT NULL` | encoded IDs |
| `payload_sha256 BLOB NOT NULL` | SHA-256 of `payload` |
| `etag TEXT`, `last_modified TEXT` | HTTP validators from the last 200 response |
| `frozen INTEGER NOT NULL DEFAULT 0` | `1`: never fetched (`cas/legacy`) |
| `changed_at TIMESTAMP NOT NULL` | last payload change |
| `checked_at TIMESTAMP NOT NULL` | last successful 200 or 304 |

`banlist_observations`, small TTL records (`PRIMARY KEY (user_id, source)`, `WITHOUT ROWID`, index on `expires_at`): `user_id`, `source` (`lols/hourly`, `lols/online`, `cas/online`), `observed_at`, `expires_at`. The hourly feed (about 16 KB) upserts with a 26 h TTL; online positives upsert with a 24 h TTL. Expired rows are deleted in bounded batches by the existing hourly maintenance.

Encoding format 1: sorted unique `int64`; the first value as a zigzag varint, every following value as the unsigned varint of its positive difference from the previous one (`encoding/binary`). Measured on the 2026-10-05 production snapshot: 1.9 bytes per ID, 6.7 MB for 3.70 M lols IDs. Decoding validates the count, strict ordering and the absence of trailing bytes; a corrupt payload is logged and ignored, and a fetchable source is then scheduled for an immediate refetch.

## Fetch cycle

Each fetchable source has a URL, a check interval (1 h for all three lols feeds) and a target (snapshot or observations). At start and on every maintenance tick a source is due when `now - checked_at >= interval`.

1. Send `If-None-Match` and `If-Modified-Since` from the stored validators.
2. `304`: update `checked_at` only.
3. `200`: stream-parse IDs with the existing tolerant format (first CSV field, header lines skipped), sort, compact, encode, hash.
4. Same hash: update validators and `checked_at`.
5. Different hash: write the new row in one transaction and publish it in memory. Log source, counts, added and removed totals (from a merge diff), payload bytes and durations; never individual IDs.
6. Shrink guard (builder's call made by the designer): when the new count is below 80 % of the stored count, keep the old payload and log at ERROR. Accept the smaller list only when the same payload hash is downloaded again at least 1 h later; the pending hash lives in memory, so a restart restarts the wait. Truncated bodies and parse errors already fail the whole fetch.

## Memory

An immutable `banlistIndex` holds one sorted `[]int64` per snapshot source and is swapped atomically. `IsKnownBanned` binary-searches each source slice (three sources, about 23 comparisons each) and then checks an observation map that holds expiry times under an `RWMutex`. Expected resident size is about 50 MB of slices plus the small map; the current map peaks at 862 MB during import.

## Check order (unchanged)

Per-chat exception, then global exception (`chat_not_spammer_overrides`, `chat_id` 0 or NULL is global; lookup failures fail closed and continue normal moderation), then the in-memory banlist, then online checks (lols API, CAS when a key is configured). Online positives become observations.

## CAS

- `NG_CAS_API_KEY` (optional). Without it the CAS check is disabled: no requests, no warnings.
- The key goes in `Authorization: Bearer`, never in the URL.
- Banned when `ok` is true and `result` is present; `ok: false` with "Record not found" means clean. The exact shapes must be confirmed before release by an operator-run probe that reads the key from hidden input and prints only the JSON responses.
- 401 or 403 means a rejected key: log one ERROR per 10 minutes with a counter, fail open.
- The export feed is removed.

## Global exceptions for the owner

- `NG_OWNER_USER_ID` (optional, 0 disables the feature). It is not a secret.
- A private-chat command, accepted only from the owner: `/global_indulgence` lists global entries with their numbers; `/global_indulgence add <user ID | @username | t.me link>` adds one through the existing `parseNotSpammerReference`; `/global_indulgence del <number>` deletes one. Rows are stored with `chat_id = 0`, which the existing lookup already treats as global. From anyone else, or outside a private chat, the command is ignored like an unknown command.
- Designer's call: a command instead of an admin-panel screen. Panel sessions reference `chats(id)` through a foreign key and re-check chat-manager rights on every action, so a chat-less owner session would need a fake `chats` row that background jobs would see.
- Designer's call: adding a global exception does not unban or unmute the user in chats where they are already restricted; it only prevents future banlist actions. Per-chat rehabilitation stays in the chat panel.
- New UI keys are translated for every locale in `resources/i18n/translations.yml`.

## Telegram polling conflict

`409 Conflict: terminated by other getUpdates request` is a distinct condition. Polling keeps retrying with backoff, does not count conflicts toward the recovery window (a restart cannot resolve them) and logs one ERROR per 5 minutes with a conflict counter. Other polling failures keep the current recovery window and exit code 1.

## Migration and rollout

1. Migration `20261026000000-banlist-snapshots.sql` creates the new tables and copies unexpired `online` and `hourly` legacy entries into `banlist_observations`. Legacy tables stay untouched in this release, so the previous image can still run.
2. On start, after migrations, a one-time idempotent conversion runs when legacy tables exist and the snapshot rows are missing: the active `cas/daily` generation becomes frozen `cas/legacy`; the active `lols/daily` generation becomes `lols/banlist` with `checked_at` in the past, so the first fetch replaces it.
3. The bot stops writing legacy tables and the per-write incremental vacuum.
4. A follow-up release drops the legacy tables and kv keys `last_daily_fetch` and `last_hourly_fetch`; offline maintenance `VACUUM` returns about 263 MB.
5. The operator adds `NG_CAS_API_KEY` through a hidden-input script and `NG_OWNER_USER_ID`; Compose passes both.

## Verification

- Codec round trips (random sets, negative IDs, `math.MinInt64` and `math.MaxInt64`, empty), corrupt payload rejection.
- Scheduler: CAS failure does not block lols; 304 and unchanged-hash paths write only `checked_at`; the shrink guard keeps the old payload; restart within the interval fetches nothing.
- Observations: TTL expiry, upsert, pruning.
- Legacy conversion from a fixture database; second run is a no-op.
- Owner command: owner allowed in private chat, non-owner and group usage ignored; add, list and delete round trip; global rows honored by `IsChatNotSpammer`.
- Polling: 409 does not end polling; other errors still exhaust the window.
- Benchmark: replacing a 5 M-ID snapshot writes at most 50 MB including WAL and finishes within 5 s.
- Production acceptance: ngbot cgroup writes below 1 GB over the first 2 h after the conversion, no hourly iowait spikes in `sar`, effective banlist counts consistent with the legacy count.

## Left to the builder

File and type names, the log rate-limit implementation, the exact HTTP header handling for servers without validators, test fixture construction, and whether `/global_indulgence` is announced through a chat-scoped `setMyCommands` for the owner (a failure there must not stop the bot).
