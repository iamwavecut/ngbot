# Operator deployment guide

## Configuration ownership

`compose.yaml` is the only Compose source. It is tracked and validated in CI. Host-specific values belong in the ignored mode-`0600` `.env`; do not create a copied Compose file. `NGBOT_DATA_PATH` is the secured host directory, mounted read-write at `/data`. `NG_DOT_PATH` is fixed to `/data` inside the container so SQLite persistence cannot silently move elsewhere.

Create the initial files and directory without exposing secret values:

```sh
install -m 0600 /dev/null .env
sed '/^[[:space:]]*#/d; /^[[:space:]]*$/d' .env.example > .env
chmod 0600 .env
sudo install -d -m 0700 -o 65532 -g 65532 /home/username/.ngbot
```

Set `NGBOT_DATA_PATH=/home/username/.ngbot`, `NG_TOKEN`, and only the LLM credential required by the selected provider. Validate permissions with `stat -c '%a %n' .env` on Linux or `stat -f '%Lp %N' .env` on macOS. Do not display the file contents in automation logs.

## Secret rotation

1. Issue the replacement credential at Telegram or the LLM provider.
2. Create `.env.next` with `install -m 0600 /dev/null .env.next`, populate it through a secure editor, and validate it with `docker compose --env-file .env.next config --quiet`. Never render the resolved configuration into shared logs.
3. Atomically replace the active file with `mv .env.next .env`; verify mode `0600` again.
4. Recreate only this service with `docker compose up -d --build ngbot`.
5. Wait for `docker compose ps` to show healthy, verify `docker compose exec ngbot ./ngbot --version`, and inspect bounded metadata-only logs.
6. Revoke the old credential only after the replacement path is healthy. If startup fails, restore the previous mode-`0600` file and recreate the service before revocation.

## Release identity and health

Set immutable metadata before building:

```sh
export NGBOT_VERSION=vX.Y.Z
export NGBOT_REVISION="$(git rev-parse HEAD)"
export NGBOT_BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
./scripts/validate-deployment.sh
docker compose build ngbot
docker compose up -d ngbot
docker compose exec ngbot ./ngbot --version
docker image inspect ngbot-ngbot --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}'
```

`/livez` means the embedded HTTP process is serving. `/readyz` becomes healthy only after all runtime components start, and becomes unhealthy before shutdown. Docker probes `/readyz`; an unexpected WebApp serving failure exits the process so `restart: unless-stopped` can recover it. Caddy must proxy only from host loopback and terminate TLS.

Compose retains five 10 MiB JSON log files. WebApp access telemetry intentionally omits query strings, client identities, request bodies, cookies, and authorization headers.

## Native execution

The native binary does not parse `.env`. Export it explicitly:

```sh
set -a
. ./.env
set +a
go run ./cmd/ngbot
```

Native execution defaults the WebApp listener to `127.0.0.1:8080`. Do not change it to a wildcard address unless it is inside an isolated container whose published port remains bound to host loopback behind the TLS proxy.
