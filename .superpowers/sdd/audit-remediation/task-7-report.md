# Task 7 report: configuration, infrastructure, observability, and operator workflows

## Outcome

Task 7 is implemented in the isolated `audit-task7` worktree. No production service, external provider, secret, or other worktree was touched. `docs/CODEBASE_MAP.md` remains unchanged.

## RED evidence

### Runtime and configuration contract

Command:

```sh
go test ./internal/config ./internal/handlers/chat ./cmd/ngbot
```

Expected failure before implementation:

```text
internal/config/config_test.go: unknown field MaxConcurrent and RequestsPerMinute
cmd/ngbot/main_test.go: undefined versionText, buildIdentity, runHealthcheck, reportWebAppFatalError
internal/handlers/chat/gatekeeper_webapp_test.go: undefined readiness, rate limiting, admission, telemetry, and fatal serving hooks
FAIL
```

The test could not compile specifically because the requested public contracts did not exist.

### Deployment contract

Command:

```sh
./scripts/validate-deployment.sh
```

Expected failure before implementation:

```text
error: pathspec 'compose.yaml' did not match any file(s) known to git
```

The active Compose file was ignored while only a `.dist` template was tracked.

## GREEN evidence

Focused command:

```sh
go test ./internal/config ./internal/handlers/chat ./cmd/ngbot
./scripts/validate-deployment.sh
```

Result: all three Go packages passed and the deployment validator exited 0.

Full command:

```sh
go test ./...
```

Result: every package passed.

Additional checks:

```sh
go vet ./...
go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...
go test -race ./...
go mod tidy -diff
go run -ldflags='-X main.version=v0.0.0-task7 -X main.revision=66bc102-task7 -X main.buildDate=2026-08-13T16:00:00Z' ./cmd/ngbot --version
```

Result: vet passed, lint reported `0 issues`, the final full race run passed, module tidy produced no diff, and the binary reported all injected identity fields. The first full race attempt exposed the existing timing-sensitive polling test once (`expected at least 3 polling attempts, got 2`); its focused race rerun and the subsequent full race rerun passed without code changes.

## Implemented controls

- Replaced the ignored copied Compose workflow with tracked `compose.yaml`; removed `compose.yaml.dist` and its ignore rule.
- Made persistence explicit: secured host `NGBOT_DATA_PATH` bind-mounts to `/data`, and Compose fixes `NG_DOT_PATH=/data`.
- Kept host-specific paths and build values in ignored mode-`0600` `.env`, with setup and atomic rotation procedures that never require printing secrets.
- Defaulted native HTTP to `127.0.0.1:8080`; Compose alone binds `0.0.0.0:8080` inside the container and publishes it on host `127.0.0.1`.
- Added `/livez`, `/readyz`, container health checks, fatal WebApp serving supervision, and exit-1 restart signaling.
- Added bounded concurrent admission, per-client request rate limiting, and metadata-only WebApp telemetry. Query strings, request bodies, authorization headers, cookies, tokens, and user identities are never logged by access telemetry.
- Preserved Task 1 framing: page CSP permits only Telegram Web; the Caddy template no longer overrides it with `X-Frame-Options: DENY` or a same-origin resource policy.
- Added log retention, read-only root filesystem, no-new-privileges, dropped capabilities, and bounded tmpfs.
- Added OCI version/revision/build-date labels, link-time binary identity, `--version`, and validation of rendered build args.
- Corrected native `.env` instructions and documented Compose-only versus application variables, locale parity, proxying, health, rotation, and release proof.
- Added CI deployment validation for environment parity, rendered persistence/listener/port/restart/log/health/build identity, and Caddy security headers.

## Remaining integration note

Release automation must set `NGBOT_VERSION`, exact `NGBOT_REVISION`, and an RFC 3339 `NGBOT_BUILD_DATE`; their `dev`/`unknown` defaults are intentionally conspicuous for local builds. Production deployment remains Task 8 and was not performed here.

The local Docker client was present, so rendered Compose validation completed, but the Docker daemon was unavailable at the configured OrbStack socket. Consequently an actual container build remains for the Task 8/full-branch gate; CI now performs that build with explicit identity arguments.
