# Comment author trust: pre-release local validation

This checkpoint records validation on 2026-09-14 with Go 1.25.13, from base `b564d78` in branch `comment-author-trust`, before committing or publishing. Subsequent release integration retains the Go 1.26.8 security update from current `master` (`77f0bda`); its CI and production evidence are separate from this initial checkpoint.

## Behavior covered

- Users, non-member commenters and sender channels use typed, per-chat trust independent of membership. One hundred safe new messages require three classifications; the first safe new message after 30 days renews trust with one classification.
- SQLite tests cover duplicate delivery, concurrency, restart, expiry, rollback on write errors, suspension, confirmed-spam resets and false-positive restoration. Legacy effective trust, checked-message bindings, votes and unfinished actions survive migration; active probation and reaction-only state do not grant trust.
- Commands, caption/rich-text controls, bot mentions, edits, reactions and empty content cannot advance or renew trust. Checked-message edit protection survives retention, expiry, renewal and trust resets.
- Context tests cover thread/chat isolation, reply and source-post priority, the five-message/24-hour/2,000-character/8,000-character limits, missing roots, stale and same-second edits, quote boundaries and bot-deletion tombstones.
- Moderation tests cover user banlist priority, manual allowlists, no-rights behavior, actual voter membership, channel voting and recovery without punishing a technical sender. A real durable-dispatcher regression preserves exhausted channel failures in the existing error queue.
- Deletion regressions distinguish successful user revokes from accepted no-op errors, preserve channel history unless explicitly deleted, resume CAPTCHA context cleanup across SQLite reopen without repeating bans, and preserve terminal permission-denied banlist fences on replay.
- Russian contextual-reply and spam fixtures verify classifier input and untrusted-history framing with deterministic LLM doubles. They do not measure a live model's classification accuracy.

## Initial checks

| Check | Result |
|---|---|
| `go test ./...` | 730 test cases passed across 19 packages |
| `go test -race ./...` | 730 test cases passed across 19 packages |
| `go test -shuffle=on ./...` | 730 test cases passed across 19 packages |
| `go vet ./...` | Passed |
| `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...` | Incomplete evidence: inherited local configuration excluded test `goconst` checks |
| `go tool gofumpt -l .` | No files reported |
| `go mod tidy -diff` | No changes |
| `git diff --check` | Clean |
| `go tool govulncheck ./...` | No reachable or imported-package vulnerabilities; four advisories in required modules whose affected code is not called |
| `./scripts/validate-deployment.sh` | Passed |
| Docker build | Passed with the pinned Go 1.25.13 builder |
| Container identity | Verified revision label, non-root user and `--version` |
| Persistent migrations | Fresh volume: 51 migrations; second maintenance run on the same volume succeeded |
| Caddy 2.10.2 validation | Valid configuration; existing formatting warning at line 22 remains unchanged |
| Independent review | No remaining findings after regression-backed corrections |

All Go commands used `GOTOOLCHAIN=go1.25.13`. An earlier concurrent normal run hit the existing one-second timeout in `TestWebAppCannotApproveWhileProviderBanCheckIsBlocked`; ten isolated repetitions and the final sequential whole-suite runs passed without modifying that test.

Release integration exposed an untracked parent `.golangci.yml` that excluded `goconst` for test files. CI correctly reported repeated test literals. The canonical command in CI and `AGENTS.md` now uses `--no-config` so local and hosted runs use the same checks; release requires a clean run of that explicit command and the complete GitHub checks.

After the fixture corrections, the uncapped Go 1.26.8 linter with `--no-config` reported zero issues, and all 577 affected test cases passed across five packages. The complete suite had already passed 730 cases in normal, race and shuffled runs on Go 1.26.8; hosted checks are repeated for the corrected commit before release.

## Container artifact

- Local tag: `ngbot:comment-author-trust-check`
- Image ID: `sha256:1bae3103cc6845a76e93a6a0ebc7076aacdef90a0bad64b3cabbf5d2c0daf01e`
- Version: `comment-author-trust-check`
- Revision label: `b564d78-local-author-trust` (explicitly a local uncommitted build, not a published Git revision)
- Build date: `1970-01-01T00:00:00Z`
- Temporary migration-test volume was removed after both passes.

## Boundaries

At this checkpoint, no commits, push, PR, deployment, production mutation or paid live LLM calls had been performed. The user subsequently authorized commits and production deployment. The generated codebase map was not edited. These local checks do not prove live Telegram behavior, real-model quality, token savings or the cause of the reported production incident. The accepted tradeoff remains: three harmless messages can earn 30 days of automatic trust. Text context expires after 24 hours; text-free checked-message bindings remain until chat deletion to preserve future edit checks.
