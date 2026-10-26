# Banlist Snapshot Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace row-per-ID banlist generations with one compressed snapshot per source, fix CAS through its authenticated `check` method, add owner-managed global exceptions, and stop the polling-conflict restart loop.

**Architecture:** Bulk sources (`lols/banlist`, `lols/scammers`, frozen `cas/legacy`) are stored as one BLOB row each in SQLite and held in memory as sorted `[]int64` slices behind an atomically swapped index. Small TTL records (hourly feed, online positives) live in a `WITHOUT ROWID` table. Every source is fetched with HTTP validators on its own schedule; unchanged lists write nothing but `checked_at`.

**Tech Stack:** Go 1.26.8 standard library (`encoding/binary`, `crypto/sha256`, `slices`, `sync/atomic`), SQLite through `modernc.org/sqlite` and `sqlx`, `rubenv/sql-migrate`, `OvyFlash/telegram-bot-api`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-26-banlist-snapshot-storage-design.md`

## Global Constraints

- Go 1.26.8, no new modules; `go mod tidy -diff` must stay empty.
- Code style per AGENTS.md: no comments, no TODOs, MixedCaps, `fmt.Errorf("op: %w", err)`, consumer-owned interfaces, table-driven tests beside code.
- Never log, print or put into URLs the bot token or the CAS key; register the CAS key with `config.RegisterSecret` and mask it in `maskConfiguration`.
- Never log individual banned IDs in bulk; log counts only.
- Snapshot writes are one statement in one transaction under the client write lock; no temp tables, no batches, no incremental vacuum on any write path.
- Exceptions are checked before the banlist and are never merged into ban sources; global scope is `chat_id = 0` (NULL also counts as global in the existing lookup).
- Legacy tables `banlist`, `banlist_entries`, `banlist_generations` stay untouched in this release; the drop happens in a follow-up release (Task 10).
- Every new UI key is added for every locale in `resources/i18n/translations.yml`.
- Commits, pushes, PRs, production config changes and releases happen only after the owner's explicit approval at execution time.
- CI gate: `test -z "$(go tool gofumpt -l .)"`, `go mod tidy -diff`, `git diff --check`, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go test -shuffle=on ./...`, `go tool golangci-lint run --no-config --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...`, `go tool govulncheck ./...`, `./scripts/validate-deployment.sh`.

## Review Focus

- A 200 response with an empty or near-empty body (CDN error page, upstream outage) must keep the stored snapshot. Test in Task 5: `TestRefreshKeepsSnapshotOnEmptyBody`.
- A server that sends no `ETag` (today `scammers.txt`) must get no empty `If-None-Match` header and must fall back to the hash comparison. Test in Task 3: `TestFetchBanlistSourceOmitsMissingValidators`.
- Feed noise (CRLF line endings, blank lines, duplicates, an `id` header, negative channel IDs) must produce the same normalized set. Test in Task 3: `TestFetchBanlistSourceParsesNoisyFeed`.
- A shutdown during a snapshot write must leave the previous row intact. Test in Task 2: `TestSaveBanlistSnapshotCanceledKeepsPreviousRow`.
- A rejected CAS key must fail open without one warning per message. Test in Task 4: `TestCASKeyRejectionIsReportedOncePerInterval`.

---

### Task 1: Snapshot codec and domain types

**Files:**
- Create: `internal/db/banlist_snapshot.go`
- Test: `internal/db/banlist_snapshot_test.go`

**Interfaces:**
- Produces:
  - `const BanlistSnapshotFormatV1 = 1`
  - `var ErrCorruptBanlistPayload = errors.New("corrupt banlist payload")`
  - `type BanlistSnapshot struct { Source string \`db:"source"\`; Format int \`db:"format"\`; IDCount int \`db:"id_count"\`; Payload []byte \`db:"payload"\`; PayloadSHA256 []byte \`db:"payload_sha256"\`; ETag sql.NullString \`db:"etag"\`; LastModified sql.NullString \`db:"last_modified"\`; Frozen bool \`db:"frozen"\`; ChangedAt time.Time \`db:"changed_at"\`; CheckedAt time.Time \`db:"checked_at"\` }`
  - `type BanlistObservation struct { UserID int64 \`db:"user_id"\`; Source string \`db:"source"\`; ObservedAt time.Time \`db:"observed_at"\`; ExpiresAt time.Time \`db:"expires_at"\` }`
  - `func NormalizeBanlistIDs(ids []int64) []int64` sorts in place and removes duplicates.
  - `func EncodeBanlistIDs(sorted []int64) ([]byte, error)` returns `ErrCorruptBanlistPayload` when input is not strictly increasing.
  - `func DecodeBanlistIDs(payload []byte, count int) ([]int64, error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestBanlistCodecRoundTrip(t *testing.T) {
	t.Parallel()
	cases := map[string][]int64{
		"empty":    {},
		"single":   {42},
		"extremes": {math.MinInt64, -1004499987047, 0, 1, 6267307735356803662, math.MaxInt64},
		"random":   randomSortedIDs(t, 100_000),
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			payload, err := EncodeBanlistIDs(ids)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := DecodeBanlistIDs(payload, len(ids))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !slices.Equal(ids, got) {
				t.Fatalf("round trip mismatch: got %d IDs, want %d", len(got), len(ids))
			}
		})
	}
}

func TestEncodeBanlistIDsRejectsUnsortedInput(t *testing.T) {
	t.Parallel()
	for _, ids := range [][]int64{{5, 5}, {5, 4}} {
		if _, err := EncodeBanlistIDs(ids); !errors.Is(err, ErrCorruptBanlistPayload) {
			t.Fatalf("EncodeBanlistIDs(%v) error = %v, want ErrCorruptBanlistPayload", ids, err)
		}
	}
}

func TestDecodeBanlistIDsRejectsCorruptPayload(t *testing.T) {
	t.Parallel()
	payload, err := EncodeBanlistIDs([]int64{1, 2, 3})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cases := map[string]struct {
		payload []byte
		count   int
	}{
		"count too high":   {payload, 4},
		"count too low":    {payload, 2},
		"truncated":        {payload[:len(payload)-1], 3},
		"zero delta":       {append(slices.Clone(payload[:1]), 0x00), 2},
		"trailing garbage": {append(slices.Clone(payload), 0x01), 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeBanlistIDs(tc.payload, tc.count); !errors.Is(err, ErrCorruptBanlistPayload) {
				t.Fatalf("error = %v, want ErrCorruptBanlistPayload", err)
			}
		})
	}
}

func TestNormalizeBanlistIDs(t *testing.T) {
	t.Parallel()
	if got := NormalizeBanlistIDs([]int64{3, 1, -5, 3, 1}); !slices.Equal(got, []int64{-5, 1, 3}) {
		t.Fatalf("NormalizeBanlistIDs = %v, want [-5 1 3]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db -run 'TestBanlistCodec|TestEncodeBanlistIDs|TestDecodeBanlistIDs|TestNormalizeBanlistIDs' -v`
Expected: FAIL, undefined `EncodeBanlistIDs`.

- [ ] **Step 3: Implement the types and codec in `internal/db/banlist_snapshot.go`**

Encoding: first value with `binary.AppendVarint`; each following value as `binary.AppendUvarint(uint64(cur) - uint64(prev))`. Unsigned wrap-around arithmetic keeps the delta between `math.MinInt64` and `math.MaxInt64` exact. Decoding reverses it with `prev + int64(delta)`, rejects a zero delta, a short payload, leftover bytes and a count mismatch, and preallocates `make([]int64, 0, count)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db -run 'TestBanlistCodec|TestEncodeBanlistIDs|TestDecodeBanlistIDs|TestNormalizeBanlistIDs' -v`
Expected: PASS.

- [ ] **Step 5: Add `BenchmarkBanlistCodec5M` and record bytes per ID**

The benchmark encodes and decodes 5,000,000 sorted IDs with gaps similar to production (random gaps averaging 1,750) and reports `b.ReportMetric(float64(len(payload))/float64(n), "bytes/id")`. Expected: below 2.5 bytes/id.

- [ ] **Step 6: Commit**

```bash
git add internal/db/banlist_snapshot.go internal/db/banlist_snapshot_test.go
git commit -m "Add banlist snapshot codec"
```

### Task 2: SQLite snapshot storage, observations and legacy conversion

**Files:**
- Create: `resources/migrations/20261026000000-add-banlist-snapshots.sql`
- Create: `internal/db/sqlite/client_banlist_legacy.go`
- Modify: `internal/db/sqlite/client_banlist.go` (replace contents)
- Modify: `internal/db/sqlite/client.go:40-48,116-135` (remove `banlistImportMutex`, `banlistCleanupBatchLocked`, `banlistCleanupBetweenBatches`; call `convertLegacyBanlist` after `migrate.Exec`)
- Test: `internal/db/sqlite/client_banlist_test.go` (replace contents), `internal/db/sqlite/client_banlist_legacy_test.go`

**Interfaces:**
- Consumes: Task 1 types and codec.
- Produces, all on `*sqliteClient`:
  - `BanlistSnapshots(ctx context.Context) ([]db.BanlistSnapshot, error)`
  - `SaveBanlistSnapshot(ctx context.Context, snapshot *db.BanlistSnapshot) error` (insert or replace the whole row for `snapshot.Source`)
  - `TouchBanlistSnapshot(ctx context.Context, source string, etag, lastModified sql.NullString, checkedAt time.Time) (bool, error)` (`false` when no row exists)
  - `BanlistObservations(ctx context.Context, now time.Time) ([]db.BanlistObservation, error)` (unexpired only)
  - `UpsertBanlistObservations(ctx context.Context, observations []db.BanlistObservation) error` (one transaction; on conflict keep the later `expires_at`)
  - `DeleteExpiredBanlistObservations(ctx context.Context, now time.Time, limit int) (int64, error)`
- Removes: `UpsertBanlist`, `ApplyBanlistSource`, `createBanlistGeneration`, `insertBanlistEntries`, `stageBanlistDelta`, `activateBanlistGeneration`, `dropBanlistDeltaTables`, `scanBanlistIDs`, `CleanupBanlistSources`, `cleanupBanlistGenerationBatch`, `reclaimBanlistStorage`, `GetBanlist` and their tests and benchmarks.

Migration Up: create `banlist_snapshots` (columns exactly as in the spec table, `source TEXT PRIMARY KEY`, `CHECK (format = 1)`, `CHECK (frozen IN (0, 1))`) and `banlist_observations (user_id INTEGER NOT NULL, source TEXT NOT NULL, observed_at TIMESTAMP NOT NULL, expires_at TIMESTAMP NOT NULL, PRIMARY KEY (user_id, source)) WITHOUT ROWID` with `idx_banlist_observations_expires ON banlist_observations(expires_at)`. Then copy legacy TTL rows:

```sql
INSERT INTO banlist_observations (user_id, source, observed_at, expires_at)
SELECT entry.user_id, generation.provider || '/' || generation.feed_type, MAX(generation.last_seen_at), MAX(generation.expires_at)
FROM banlist_entries AS entry
JOIN banlist_generations AS generation ON generation.id = entry.generation_id
WHERE generation.active = 1
	AND generation.feed_type IN ('online', 'hourly')
	AND generation.expires_at > CURRENT_TIMESTAMP
GROUP BY entry.user_id, generation.provider, generation.feed_type;
```

Migration Down: drop both new tables and the index.

`convertLegacyBanlist(ctx)`: when `banlist_generations` exists, for each pair (`cas/daily` → `cas/legacy`, frozen, `checked_at` = generation `last_seen_at`) and (`lols/daily` → `lols/banlist`, not frozen, `checked_at` = Unix epoch so the first fetch replaces it) whose target row is missing, read `SELECT user_id FROM banlist_entries WHERE generation_id = ? ORDER BY user_id` for the active generation, encode, and save. It never writes legacy tables.

- [ ] **Step 1: Write the failing tests**

- `TestBanlistSnapshotsMigrationCreatesTablesAndCopiesTTLRows`: seed legacy generations (`lols/online` unexpired, `cas/online` expired, `lols/daily` active) through the old schema at migration `20260715190000`, apply all migrations, assert exactly the unexpired online row is in `banlist_observations` with source `lols/online`.
- `TestSaveAndLoadBanlistSnapshot`: save `lols/banlist` with 3 IDs, load, decode, compare every field.
- `TestTouchBanlistSnapshotUpdatesOnlyValidators`: touch changes `etag`, `last_modified`, `checked_at`; `payload`, `changed_at` stay byte-equal; touching a missing source returns `false, nil`.
- `TestSaveBanlistSnapshotCanceledKeepsPreviousRow`: save version A, call save of version B with an already canceled context, assert an error and that the stored payload is still A.
- `TestBanlistObservationsExpiry`: upsert two rows (one expires in the past), `BanlistObservations(now)` returns one; an upsert with an earlier `expires_at` does not shorten the stored one; `DeleteExpiredBanlistObservations(now, 1)` deletes one row and reports `1`.
- `TestConvertLegacyBanlistCreatesSnapshotsOnce`: seed `cas/daily` with IDs `{9, -3, 7}` and `lols/daily` with `{1, 2}`; open the client; assert `cas/legacy` decodes to `{-3, 7, 9}` with `Frozen == true`, `lols/banlist` decodes to `{1, 2}` with `CheckedAt` at the epoch; reopen the client and assert `ChangedAt` of both rows is unchanged.
- `TestConvertLegacyBanlistWithoutLegacyTablesIsNoop`: on a database where the legacy tables were dropped, opening succeeds and no snapshot rows exist.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db/sqlite -run 'TestBanlistSnapshots|TestSaveAndLoadBanlistSnapshot|TestTouchBanlistSnapshot|TestSaveBanlistSnapshotCanceled|TestBanlistObservations|TestConvertLegacyBanlist' -v`
Expected: FAIL, undefined methods.

- [ ] **Step 3: Write the migration and the methods**

`SaveBanlistSnapshot` is one `INSERT ... ON CONFLICT(source) DO UPDATE SET ...` inside `BeginTxx`/`Commit` under `c.mutex.Lock()`. `UpsertBanlistObservations` uses one transaction and `ON CONFLICT(user_id, source) DO UPDATE SET observed_at = excluded.observed_at, expires_at = MAX(expires_at, excluded.expires_at)`. Reads take `c.mutex.RLock()`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db/sqlite -run 'TestBanlistSnapshots|TestSaveAndLoadBanlistSnapshot|TestTouchBanlistSnapshot|TestSaveBanlistSnapshotCanceled|TestBanlistObservations|TestConvertLegacyBanlist' -v`
Expected: PASS.

- [ ] **Step 5: Add `BenchmarkSaveBanlistSnapshot5M`**

Save a 5,000,000-ID payload twice into a temporary database and report `ns/op` plus the growth of `bot.db` plus `bot.db-wal` after `PRAGMA wal_checkpoint(TRUNCATE)` as a custom metric. Expected: below 5 s per save and below 50 MB growth per save after the first.

- [ ] **Step 6: Run the whole package**

Run: `go test ./internal/db/... && go vet ./internal/db/...`
Expected: PASS. `go build ./cmd/ngbot` fails until Task 5, because `sqliteClient` no longer satisfies the old `banStore`; the moderation package itself still compiles.

- [ ] **Step 7: Commit**

```bash
git add resources/migrations/20261026000000-add-banlist-snapshots.sql internal/db/sqlite/client.go internal/db/sqlite/client_banlist.go internal/db/sqlite/client_banlist_test.go internal/db/sqlite/client_banlist_legacy.go internal/db/sqlite/client_banlist_legacy_test.go
git commit -m "Store banlists as snapshots in SQLite"
```

### Task 3: Conditional fetch of one source

**Files:**
- Create: `internal/handlers/moderation/banlist_sources.go`
- Modify: `internal/handlers/moderation/ban_service_fetch.go` (keep `fetchURLWithRetry` retry semantics, return `[]int64`)
- Test: `internal/handlers/moderation/banlist_sources_test.go`

**Interfaces:**
- Consumes: `db.NormalizeBanlistIDs`.
- Produces:
  - `type banlistSourceSpec struct { name string; url string; interval time.Duration; observationTTL time.Duration }` (`observationTTL > 0` means the source feeds observations)
  - `func defaultBanlistSources() []banlistSourceSpec` returning `lols/banlist` (`banlistURL`, 1h), `lols/scammers` (`scammersURL`, 1h), `lols/hourly` (`banlistURLHourly`, 1h, TTL `banlistHourlyTTL` = 26h)
  - `type banlistFetch struct { NotModified bool; IDs []int64; ETag string; LastModified string }`
  - `func fetchBanlistSource(ctx context.Context, client *http.Client, url, etag, lastModified string) (banlistFetch, error)` with up to `banServiceMaxRetries` attempts; `IDs` normalized.

- [ ] **Step 1: Write the failing tests**

- `TestFetchBanlistSourceNotModified`: server asserts `If-None-Match: "v1"` and `If-Modified-Since: Sun, 25 Oct 2026 23:21:58 GMT`, replies 304; result `NotModified == true`, `IDs == nil`.
- `TestFetchBanlistSourceOmitsMissingValidators`: called with empty validators; server asserts both headers are absent and replies 200 without `ETag`; result `ETag == ""`.
- `TestFetchBanlistSourceParsesNoisyFeed`: body `"id\r\n5\r\n\r\n-1001\r\n5\r\n3,extra\r\n"`; result `IDs == []int64{-1001, 3, 5}`, validators copied from the response headers.
- `TestFetchBanlistSourceFailsOnGarbage`: body `"5\nnot-a-number\n"` returns an error and no IDs.
- `TestFetchBanlistSourceFailsOnTruncatedBody`: server declares `Content-Length: 100` and closes after 10 bytes; error.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/moderation -run 'TestFetchBanlistSource' -v`
Expected: FAIL, undefined `fetchBanlistSource`.

- [ ] **Step 3: Implement `fetchBanlistSource`**

Send `If-None-Match` and `If-Modified-Since` only when non-empty. Parse with the existing CSV logic into `[]int64` (preallocate 1,024), then `db.NormalizeBanlistIDs`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/moderation -run 'TestFetchBanlistSource' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/moderation/banlist_sources.go internal/handlers/moderation/banlist_sources_test.go internal/handlers/moderation/ban_service_fetch.go
git commit -m "Fetch banlist sources with HTTP validators"
```

### Task 4: In-memory index, observations and online checks including CAS

**Files:**
- Create: `internal/handlers/moderation/banlist_index.go`
- Modify: `internal/handlers/moderation/ban_service.go` (online checkers, `checkCASBan`)
- Test: `internal/handlers/moderation/banlist_index_test.go`, `internal/handlers/moderation/ban_service_test.go`

**Interfaces:**
- Produces:
  - `type banlistIndex struct { sources map[string][]int64 }`; `func (i *banlistIndex) contains(userID int64) bool`; `func (i *banlistIndex) with(source string, ids []int64) *banlistIndex` (copy-on-write); `func (i *banlistIndex) count(source string) int`
  - `type onlineBanChecker struct { name string; check func(ctx context.Context, client *http.Client, userID int64) (bool, error) }`
  - `func defaultOnlineBanCheckers(casAPIKey string) []onlineBanChecker` (lols always; CAS only when the key is non-empty)
  - `func checkCASBan(ctx context.Context, client *http.Client, urlTemplate, apiKey string, userID int64) (bool, error)`
  - `var errCASKeyRejected = errors.New("CAS API key rejected")`
  - `const casKeyRejectedLogInterval = 10 * time.Minute`

- [ ] **Step 1: Write the failing tests**

- `TestBanlistIndexContainsAcrossSources`: sources `{"a": {1, 5}, "b": {-3}}`; `contains(5)`, `contains(-3)` true; `contains(2)` false; `with("a", {2})` returns an index where `contains(2)` is true and `contains(5)` is false while the original index is unchanged.
- `TestCheckCASBanSendsBearerKey`: server asserts `Authorization: Bearer test-key` and no `api_key` query parameter; replies `{"ok":true,"result":{"offenses":1}}` for `123` and `{"ok":false,"description":"Record not found."}` for `456`; expects banned and clean.
- `TestCheckCASBanRejectedKey`: server replies 401 with `{"ok":false,"error_code":401,"description":"API key required"}`; error is `errCASKeyRejected`, and the error text contains neither the key nor the URL query.
- `TestDefaultOnlineBanCheckersWithoutCASKey`: only `lols` is returned for an empty key.
- `TestCASKeyRejectionIsReportedOncePerInterval`: with a fake clock, 50 checks within 10 minutes produce one ERROR entry (capture with a `logrus` test hook) and `CheckBan` returns `false, nil` each time.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/moderation -run 'TestBanlistIndex|TestCheckCASBan|TestDefaultOnlineBanCheckers|TestCASKeyRejection' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the index and the CAS checker**

`contains` uses `slices.BinarySearch` per source. Remove `casExportURL` and the CAS entry of `defaultBanlistProviders`; online checks come from `defaultOnlineBanCheckers`. `checkCASBan` maps 401 and 403 to `errCASKeyRejected` and decodes `{ok bool, result json.RawMessage}`; banned means `ok && len(result) > 0 && string(result) != "null"`. `CheckBan` logs `errCASKeyRejected` at ERROR at most once per `casKeyRejectedLogInterval` with a counter and logs other provider errors as today.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/moderation -run 'TestBanlistIndex|TestCheckCASBan|TestDefaultOnlineBanCheckers|TestCASKeyRejection' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/moderation/banlist_index.go internal/handlers/moderation/banlist_index_test.go internal/handlers/moderation/ban_service.go internal/handlers/moderation/ban_service_test.go
git commit -m "Check CAS with an API key and index banlists in memory"
```

### Task 5: Refresh orchestration, ban service lifecycle and CAS configuration

**Files:**
- Modify: `internal/handlers/moderation/ban_service.go` (store interface, `NewBanService`, `Start`, `IsKnownBanned`, observations, remove map and generation code)
- Modify: `internal/handlers/moderation/ban_service_fetch.go` (replace `refreshKnownBanned`, `FetchKnownBannedDaily`, `FetchKnownBanned`, kv timestamp helpers)
- Modify: `internal/config/config.go` (add `CAS CAS` with `APIKey string \`env:"CAS_API_KEY"\``)
- Modify: `cmd/ngbot/main.go:203-207,422,544-558` (`config.RegisterSecret(cfg.CAS.APIKey)`, mask `cfg.CAS.APIKey`, pass options)
- Modify: `compose.yaml` (`NG_CAS_API_KEY: ${NG_CAS_API_KEY:-}`)
- Test: `internal/handlers/moderation/ban_service_test.go`, `internal/handlers/moderation/ban_service_retention_integration_test.go`, `cmd/ngbot/main_test.go`

**Interfaces:**
- Consumes: Task 2 store methods, Task 3 `fetchBanlistSource` and `defaultBanlistSources`, Task 4 index and checkers.
- Produces:
  - `type BanServiceOptions struct { CASAPIKey string }`
  - `func NewBanService(bot *api.BotAPI, store banStore, options BanServiceOptions) BanService`
  - `banStore` gains the six Task 2 methods and loses `GetKV`, `SetKV`, `ApplyBanlistSource`, `CleanupBanlistSources`, `GetBanlist`
  - `func (s *defaultBanService) refreshDueSources(ctx context.Context, now time.Time)`
  - `const banlistShrinkThreshold = 0.8`, `const banlistShrinkConfirmDelay = time.Hour`, `const banlistObservationPruneBatch = 10_000`

Refresh rules (from the spec): a snapshot source is due when `now - CheckedAt >= interval` or when it has no row or its payload failed to decode. 304 or equal SHA-256 → `TouchBanlistSnapshot`. A changed payload with `newCount < 0.8 * storedCount` is held: remember `{hash, firstSeen}` in memory and apply only when the same hash arrives again at least `banlistShrinkConfirmDelay` later. Otherwise `SaveBanlistSnapshot` then swap the index. Observation sources upsert every ID with `ExpiresAt = now + observationTTL`. Frozen sources are never fetched. Online positives upsert one observation with `banlistOnlineTTL` (24h) through the existing queue. The hourly ticker prunes expired observations in batches of `banlistObservationPruneBatch` until fewer are deleted, then calls `refreshDueSources`. Start loads snapshots and observations before serving `IsKnownBanned`, then refreshes in the background.

- [ ] **Step 1: Rewrite the fake store and write the failing tests**

Replace `recordingBanStore` with an in-memory implementation of the new `banStore` that counts `SaveBanlistSnapshot` and `TouchBanlistSnapshot` calls. Tests:
- `TestRefreshIndependentSources`: `lols/banlist` server returns 200, CAS checker fails; snapshot saved once and `IsKnownBanned` sees the IDs.
- `TestRefreshNotModifiedOnlyTouches`: stored snapshot with validators, server 304; `saves == 0`, `touches == 1`.
- `TestRefreshUnchangedHashOnlyTouches`: server 200 with the same IDs and no validators; `saves == 0`, `touches == 1`.
- `TestRefreshSkipsSourceCheckedWithinInterval`: `CheckedAt = now - 30m`; no HTTP request is made.
- `TestRestartDoesNotRefetchFreshSources`: Start twice on the same store within the interval; the server sees one request per source in total.
- `TestRefreshKeepsSnapshotOnEmptyBody`: stored 1,000 IDs, server 200 with an empty body; snapshot unchanged and `IsKnownBanned` still true for a stored ID.
- `TestShrinkGuardNeedsConfirmation`: stored 1,000 IDs, server returns 500 IDs; first refresh keeps 1,000; a refresh 30 minutes later keeps 1,000; a refresh 61 minutes after the first applies 500.
- `TestFrozenSourceIsNeverFetched`: a stored `cas/legacy` snapshot with `Frozen` triggers no request and stays in the index.
- `TestCorruptSnapshotIsRefetched`: a stored payload that fails decoding is skipped at load and fetched on the first refresh.
- `TestObservationsExpire`: an online positive is visible until its 24h TTL and invisible after; the prune loop deletes it.
- `TestHourlyFeedCreatesObservations`: `lols/hourly` IDs are upserted with `ExpiresAt == now + 26h`.
- Keep and adapt `TestCheckBanRacesProvidersAndPersistsOnlinePositive`, `TestCheckBanPublishesOnlinePositiveBeforeBlockedPersistence`, `TestCheckBanFailsOpenOnProviderErrors`, `TestCheckBanReturnsCallerCancellation`, retention and restriction tests; delete tests of removed generation code.
- `cmd/ngbot`: extend the existing `maskConfiguration` test so a non-empty `CAS.APIKey` becomes `[REDACTED]`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/moderation ./cmd/ngbot -v -run 'TestRefresh|TestRestartDoesNotRefetch|TestShrinkGuard|TestFrozenSource|TestCorruptSnapshot|TestObservations|TestHourlyFeed|TestCheckBan|Mask'`
Expected: FAIL.

- [ ] **Step 3: Implement the orchestration, lifecycle, config and wiring**

Each refresh logs one DEBUG line per source with `source`, `result` (`not_modified`, `unchanged`, `changed`, `held_shrink`, `failed`), `count`, `added`, `removed` (counted by one merge pass over the old and new sorted slices), `payload_bytes`, `fetch_duration` and `apply_duration`. Update every `NewBanService` call site in tests to pass `BanServiceOptions{}`.

- [ ] **Step 4: Run the packages and the race detector**

Run: `go test ./internal/handlers/moderation/... ./internal/config/... ./cmd/ngbot/... && go test -race ./internal/handlers/moderation/...`
Expected: PASS.

- [ ] **Step 5: Validate the deployment file**

Run: `./scripts/validate-deployment.sh`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/moderation internal/config/config.go cmd/ngbot/main.go cmd/ngbot/main_test.go compose.yaml
git commit -m "Refresh banlist sources independently from snapshots"
```

### Task 6: Owner command for global exceptions

**Files:**
- Create: `internal/handlers/admin/global_indulgence.go`
- Modify: `internal/handlers/admin/admin.go:22-34,75-87,172-196` (`ownerUserID` field, constructor parameter, command case)
- Modify: `internal/config/config.go` (`OwnerUserID int64 \`env:"OWNER_USER_ID"\``)
- Modify: `cmd/ngbot/main.go:405,427,615-660` (pass owner ID; announce the owner command for `api.NewBotCommandScopeChat(ownerUserID)`, logging a failure at WARN without returning it)
- Modify: `compose.yaml` (`NG_OWNER_USER_ID: ${NG_OWNER_USER_ID:-0}`)
- Modify: `resources/i18n/translations.yml`, `internal/i18n/translations_consistency_test.go:82-100` (add the new keys to the admin completeness list)
- Test: `internal/handlers/admin/global_indulgence_test.go`

**Interfaces:**
- Consumes: existing `adminStore` methods `CreateChatNotSpammerOverride`, `ListChatNotSpammerOverrides`, `CountChatNotSpammerOverrides`, `GetChatNotSpammerOverride`, `DeleteChatNotSpammerOverride`; `parseNotSpammerReference`.
- Produces:
  - `func NewAdmin(s bot.Service, botAPI *api.BotAPI, store adminStore, stats handlersbase.StatsStore, banService moderation.BanService, ownerUserID int64) *Admin`
  - `const globalIndulgenceCommand = "global_indulgence"`, `const globalIndulgenceChatID int64 = 0`, `const globalIndulgenceListLimit = 50`
  - `func (a *Admin) handleGlobalIndulgenceCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User, lang string) (bool, error)`

New i18n keys, translated for every locale: `"Global indulgence"`, `"No global indulgence entries yet"`, `"Global indulgence entry added"`, `"Global indulgence entry deleted"`, `"Usage: /global_indulgence add <user ID, @username or t.me link> or /global_indulgence del <entry ID>"`. Reuse `"Invalid input"`.

- [ ] **Step 1: Write the failing tests**

Table-driven `TestGlobalIndulgenceCommand` with a fake store and a recording sender:
- owner, private chat, no arguments, two stored global rows → one message listing `#<id>` and the reference label for both;
- owner, `add 12345` → a row with `ChatID == 0`, `MatchType == user_id`, `MatchValue == "12345"`, `CreatedByUserID == owner`, reply "Global indulgence entry added", and no call to the ban service (no unban or unmute);
- owner, `add @Some_User` → `MatchType == username`, normalized value;
- owner, `del <id>` of a global row → deleted, reply "Global indulgence entry deleted";
- owner, `del <id>` of a per-chat row → nothing deleted, reply "Invalid input";
- owner, `add !!!` → nothing stored, reply "Invalid input";
- non-owner in private chat → returns `true, nil`, no store call, no message;
- owner in a group chat → returns `true, nil`, no store call;
- owner ID 0 configured → every sender is ignored.
- `TestGlobalIndulgenceHonoredByLookup` in `internal/db/sqlite/client_not_spammer_overrides_test.go`: a row created with `ChatID 0` makes `IsChatNotSpammer(anyChat, userID, "")` true.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/admin ./internal/db/sqlite -run 'TestGlobalIndulgence' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the command, constructor change, wiring, compose and translations**

Update every existing `NewAdmin` call site in tests to pass an owner ID.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/admin/... ./internal/i18n/... ./internal/db/sqlite/... ./cmd/ngbot/...`
Expected: PASS, including `TestTranslationsKeysAreUsedAndComplete` and `TestAdminPanelTranslationsAreCompleteForSupportedLocales`.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/admin internal/config/config.go cmd/ngbot/main.go compose.yaml resources/i18n/translations.yml internal/i18n/translations_consistency_test.go internal/db/sqlite/client_not_spammer_overrides_test.go
git commit -m "Let the bot owner manage global exceptions"
```

### Task 7: Polling conflict handling

**Files:**
- Modify: `internal/bot/polling.go:61-97`
- Test: `internal/bot/polling_test.go`

**Interfaces:**
- Produces: `func isPollingConflict(err error) bool` (true for `*api.Error` with `Code == http.StatusConflict`), `const pollingConflictLogInterval = 5 * time.Minute`.

- [ ] **Step 1: Write the failing tests**

- `TestPollingConflictDoesNotExhaustRecoveryWindow`: fetcher returns `&api.Error{Code: 409, Message: "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}` for 50 calls, then one update; options `RecoveryWindow: 5 * time.Millisecond`, `InitialBackoff` and `MaxBackoff: time.Millisecond`; the update arrives and the error channel stays empty.
- `TestPollingNonConflictErrorStillExhaustsRecoveryWindow`: the same with `errors.New("network down")` produces a `*PollingRecoveryError`.
- `TestPollingConflictLogIsRateLimited`: with a `logrus` test hook, 50 conflicts within one interval produce one ERROR entry whose fields include `conflicts`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bot -run 'TestPollingConflict|TestPollingNonConflict' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

On a conflict: count it, log ERROR "another process is polling with this bot token" at most once per `pollingConflictLogInterval` with the count, set `lastHealthyPollAt = time.Now()`, wait the backoff, continue.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bot/... && go test -race ./internal/bot/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bot/polling.go internal/bot/polling_test.go
git commit -m "Keep polling through getUpdates conflicts"
```

### Task 8: Documentation and full gate

**Files:**
- Modify: `AGENTS.md` (Query Layer concurrency: replace the generation import sentence with the snapshot rule; External Services: CAS key, owner command; add `NG_CAS_API_KEY` and `NG_OWNER_USER_ID`)

- [ ] **Step 1: Update AGENTS.md**

Replace "Banlist imports write an inactive generation in short batches, release the lock between batches, atomically activate the generation, then garbage-collect retired entries in bounded transactions." with: "Banlists are stored as one encoded snapshot per source (`banlist_snapshots`) and small TTL records (`banlist_observations`). Sources are fetched independently with HTTP validators; an unchanged list writes only `checked_at`, a changed list is written in one short transaction, and a list that shrinks below 80 % needs a confirming download at least 1 h later."

- [ ] **Step 2: Run the full CI gate**

Run each command from Global Constraints.
Expected: every command exits 0; `gofumpt -l` prints nothing.

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "Document banlist snapshot storage"
```

### Task 9: Release and production acceptance (owner approval required)

- [ ] **Step 1: Confirm the CAS response shapes**

Give the owner a probe script that reads the key with `read -rs`, calls `check` with `Authorization: Bearer` for one ID taken from `cas/legacy` and for the owner's own ID, and prints only the two JSON bodies. If the shapes differ from Task 4's test fixtures, update `checkCASBan` and the fixtures before the release.

- [ ] **Step 2: Add the configuration**

The owner adds `NG_CAS_API_KEY` through a hidden-input script modeled on the 2026-10-26 token rotation script (replace one line, keep mode 600, never print the value) and sets `NG_OWNER_USER_ID`.

- [ ] **Step 3: Release**

Run: `sudo -n env ... ./scripts/release.sh release` on the production host after approval. Expected: release verified, migration `20261026000000-add-banlist-snapshots.sql` applied, container healthy.

- [ ] **Step 4: Verify the conversion and the first refresh**

Expected in logs: `cas/legacy` and `lols/banlist` snapshots created once; the first `lols/banlist` refresh saved a changed snapshot; `lols/scammers` and `lols/hourly` loaded; zero ERROR entries apart from expected provider failures.

- [ ] **Step 5: Measure**

Sample the ngbot cgroup `io.stat` `wbytes` at start and after 2 h. Expected: below 1 GB written after the conversion; `sar -d` shows no hourly 40-60 MB/s windows; `/proc/<pid>/status` VmRSS below 300 MB; `docker inspect` RestartCount 0.

- [ ] **Step 6: Exercise the owner command**

The owner runs `/global_indulgence` in a private chat. Expected: the list reply appears; an add and a delete round trip work; non-owner usage is ignored.

- [ ] **Step 7: Record the outcome in Manifold `ngbot-prod-health-2026-10-26`**

### Task 10: Follow-up release, drop the legacy tables

- [ ] **Step 1: Add migration `resources/migrations/<timestamp>-drop-legacy-banlist.sql`**

Up: `DROP TABLE banlist_entries; DROP TABLE banlist_generations; DROP TABLE banlist; DELETE FROM kv_store WHERE key IN ('last_daily_fetch', 'last_hourly_fetch');`. Down: recreate the three empty tables with the schema of `20260715190000-cutover-banlist-generations.sql`.

- [ ] **Step 2: Test**

`TestDropLegacyBanlistMigration`: after all migrations the three tables do not exist and the snapshot tables keep their rows. `convertLegacyBanlist` stays a no-op (covered by `TestConvertLegacyBanlistWithoutLegacyTablesIsNoop`).

- [ ] **Step 3: Release with approval and verify**

Expected: offline maintenance `VACUUM` reduces `bot.db` by roughly 263 MB; the banlist counts in the startup log match the previous release.
