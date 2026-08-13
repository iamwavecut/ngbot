# Task 6 — SQLite/cache integrity, migrations, and retention

| Area | Outcome |
| --- | --- |
| ⚙️ Settings | SQLite now normalizes once inside `CommitSettings`, increments persisted `settings_revision`, returns the exact committed snapshot, and the service publishes only a newer revision. |
| 👥 Member cache | Insert/delete mutations increment a cache revision; asynchronous warmup discards its loaded snapshot when a mutation happened while the query was in flight. |
| 🧹 Retention | BanService startup runs one bounded cleanup. Challenged-message bindings and processed recent joiners retain 30 days; terminal spam cases retain 90 days. Each table is limited independently to 500 deletions per startup. |
| 🔗 References | Cleanup preserves challenged messages referenced by non-terminal spam cases, unprocessed recent joiners, non-terminal/recent spam cases, and terminal cases with queued report artifacts. |
| 🗃️ Future durable queues | `CleanupRetainedRecords(ctx, now, limit)` is the generic startup hook. Task 5 can extend the adapter behind this hook without Task 6 inventing or naming inbox/dead-letter tables. |
| ✅ Foreign keys | Startup and maintenance validation now checks `rows.Err()` and always closes the cursor. |
| ↩️ Down safety | The application migration planner rejects every Down direction before execution. The new migration's own Down removes only derived revision/index state and preserves all domain rows under foreign keys. |
| 🚦 Banlist writer | The existing writer-yield test now uses exact first-batch/between-batch barriers instead of sleeps, while retaining the assertion that an ordinary write completes between cleanup batches. |

## TDD evidence

| Slice | RED | GREEN |
| --- | --- | --- |
| Settings normalization, commit order, member deletion | `go test ./internal/bot -run 'Test(SettingsCachePublishesNormalizedCommittedSnapshot\|ConcurrentSettingsWritesPublishLatestCommit\|MemberWarmupDoesNotResurrectConcurrentDeletion)$' -count=1 -v` — all three failed with the expected cache divergence/resurrection assertions. | Same command — all three passed. |
| Bounded retention and restart | `go test ./internal/db/sqlite -run 'Test(CleanupRetentionHonorsCutoffsReferencesAndBatchLimit\|RetentionCleanupRunsAfterCrashRestart)$' -count=1 -v` — compile failed because the cleanup contract/constants did not exist. | Same command — both passed. |
| FK iterator error | `go test ./internal/db/sqlite -run '^TestForeignKeyValidationReturnsIterationError$' -count=1 -v` — compile failed because the validator did not exist. | Same command — passed. |
| Safe application Down behavior | `go test ./internal/db/sqlite -run '^TestApplicationMigrationPlannerRejectsDownWithoutChangingData$' -count=1 -v` — compile failed because the safe planner/sentinel did not exist. | Same command — passed. |
| Deterministic banlist writer barrier | `go test ./internal/db/sqlite -run '^TestBanlistCleanupYieldsToQueuedOrdinaryWriter$' -count=1 -v` — compile failed because the explicit cleanup barriers did not exist. | Same command — passed without timing sleeps. |

## Migration and restart fixtures

| Fixture | Proof |
| --- | --- |
| `20260813010000-repair-sqlite-integrity.sql` | Adds `settings_revision` and retention indexes; raw Up→Down with `PRAGMA foreign_keys=ON` preserves `chats`, challenged messages, recent joiners, and spam cases. |
| Cutoff boundaries | Exact 30/90-day cutoff rows are deleted; rows one second newer remain. |
| Batch bounds | First pass with limit 1 removes exactly one eligible row from each table; the next pass removes the remaining eligible rows. |
| Restart cleanup | Stale committed rows survive close/reopen, then the BanService startup cleanup hook removes them, demonstrating crash-safe durable cleanup rather than in-memory bookkeeping. |

## Verification

| Command | Result |
| --- | --- |
| `go test ./... -count=1` | PASS, all packages. |
| `go test -race ./internal/bot ./internal/db/sqlite -count=1` | PASS (`internal/bot` 9.874s, `internal/db/sqlite` 54.436s). |
| `go vet ./...` | PASS. |
| `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...` | PASS, `0 issues.` |
| `go mod tidy -diff` | PASS, no module changes required. |
| `git diff --check` | PASS. |

## Known verification constraint

Two full `go test -race ./... -count=1` attempts reached all Task 6 packages successfully but failed pre-existing Task 3 deadline-sensitive tests `TestJoinQueryResponseTimeoutIsDurablyActionable` and/or `TestAmbiguousWebAppResponseWaitsForBanCheckAndNeverFallsBack` while several worktrees were concurrently running SQLite race suites. Both tests passed together when rerun directly under race. The complete non-race suite and the full race suites for the modified `internal/bot` and `internal/db/sqlite` packages pass.
