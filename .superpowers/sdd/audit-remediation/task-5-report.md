# Task 5 report: durable Telegram update delivery

## Outcome

- Added a SQLite-backed Telegram update inbox and unresolved failure ledger.
- Polling persists each update before advancing the Telegram offset or handing it to execution.
- Durable execution preserves per-chat ordering, bounds global workers and pending memory, deduplicates update IDs, and recovers interrupted work on restart.
- A single persisted-due scheduler wakes at the next `available_at`, recovers stale processing leases and future retries after restart, responds to newly persisted rows, runs bounded retention, and shuts down without per-retry goroutines.
- Retryable outcomes use bounded exponential backoff. Terminal, exhausted, malformed, stale security-relevant, and ambiguous panic outcomes remain operator-visible in `telegram_update_failures`.
- LLM and moderation-capability outages are typed retryable failures. Exhausted LLM checks quarantine the author only when moderation rights are known available; known no-rights mode performs no unsafe Telegram action and retains the failure record.
- Existing Gatekeeper action leases remain the idempotent side-effect boundary. The always-on banlist guard now owns a dedicated moderation action fence with a stable ban deadline and explicit `started → banned → completed/reconciliation` phases, so an ambiguous crash after Telegram success is never blindly re-banned.
- Completed inbox rows retain seven days; dead-letter rows retain thirty days. Pending, processing, and retry rows are never retention-deleted.

## TDD evidence

| Cycle | RED | GREEN |
| --- | --- | --- |
| Migration | Missing `telegram_update_inbox` table | New inbox/failure migration applied |
| Store | Missing enqueue, dedupe, claim, retry, recovery, dead-letter APIs | Real SQLite tests pass |
| Failure policy | Missing typed SQLite/Telegram/LLM/capability classification | Classification table tests pass |
| Executor | Missing durable dispatcher | Restart, duplicate, retry order, panic, exhaustion tests pass |
| Poller | Malformed updates dropped and offset advanced before persistence | Persist-before-offset and malformed delivery tests pass |
| Security degradation | Stale edits, LLM failure, and capability unknown silently succeeded | Durable terminal/retryable outcomes and no-rights-safe degradation tests pass |
| Saturation/retention | No durable saturation or cleanup coverage | Persisted saturation and selective retention tests pass |
| Persisted scheduler | Future retries required startup timing and per-retry timers | Future-due restart, 4,096-chat saturation, long-running retention, and clean wake tests pass |
| SQLite transitions | Busy claim/complete/dead-letter errors could strand processing state | Injected busy-transition tests recover each phase durably |
| Security timestamps | Membership, join-request, and reaction events bypassed the stale policy | Stale security events dead-letter; callbacks do not inherit their message's age |
| Moderation fence | Banlist guard could repeat Telegram ban after an ambiguous crash | Stable-deadline fence moves ambiguity to reconciliation and proves one Telegram ban |

## Verification

- `go test ./... -count=1`
- `go test -race ./internal/bot ./internal/db/sqlite ./internal/handlers/chat ./internal/handlers/moderation -count=1`
- `go vet ./...`
- `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...` (`0 issues`)
- `git diff --check`

## Residual boundary

The inbox provides durable at-least-once delivery. Exactly-once external effects still require the owning workflow's persisted action fence. Gatekeeper uses the Task 3 leases, the banlist guard uses `moderation_action_fences`, and moderation resolution uses durable case transitions. New external-effect handlers must not treat inbox completion alone as an idempotency mechanism. Ambiguous ban outcomes are intentionally retained for operator reconciliation rather than retried.
