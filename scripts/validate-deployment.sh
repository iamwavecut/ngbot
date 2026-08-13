#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_root"

if [ ! -f compose.yaml ] || git check-ignore -q compose.yaml || ! git ls-files --error-unmatch compose.yaml >/dev/null 2>&1; then
	echo "compose.yaml must be the tracked deployment source" >&2
	exit 1
fi
if [ -e compose.yaml.dist ]; then
	echo "compose.yaml.dist must be replaced by the tracked active compose.yaml" >&2
	exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
	echo "docker is required to validate the deployment configuration" >&2
	exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
	echo "jq is required to validate the rendered deployment configuration" >&2
	exit 1
fi

validation_dir=$(mktemp -d)
trap 'rm -rf "$validation_dir"' EXIT HUP INT TERM
install -d -m 0700 "$validation_dir/data"
validation_port=19090

NG_TOKEN=validation-token \
NGBOT_DATA_PATH="$validation_dir/data" \
NGBOT_WEBAPP_HOST_PORT="$validation_port" \
NGBOT_VERSION=v0.0.0-validation \
NGBOT_REVISION=0123456789abcdef \
NGBOT_BUILD_DATE=2026-08-13T16:00:00Z \
	docker compose -f compose.yaml config --format json >"$validation_dir/rendered.json"

jq -e --arg data "$validation_dir/data" --arg port "$validation_port" '
  .services.ngbot.environment.NG_DOT_PATH == "/data" and
  .services.ngbot.environment.NG_GATEKEEPER_WEBAPP_LISTEN_ADDR == "0.0.0.0:8080" and
  any(.services.ngbot.volumes[]; .type == "bind" and .source == $data and .target == "/data") and
  any(.services.ngbot.ports[]; .host_ip == "127.0.0.1" and .target == 8080 and .published == $port) and
  .services.ngbot.restart == "unless-stopped" and
  .services.ngbot.logging.driver == "json-file" and
  .services.ngbot.logging.options["max-size"] == "10m" and
  .services.ngbot.logging.options["max-file"] == "5" and
  .services.ngbot.healthcheck.test == ["CMD", "./ngbot", "--healthcheck=http://127.0.0.1:8080/readyz"] and
  .services.ngbot.build.args.VERSION == "v0.0.0-validation" and
  .services.ngbot.build.args.REVISION == "0123456789abcdef" and
  .services.ngbot.build.args.BUILD_DATE == "2026-08-13T16:00:00Z"
' "$validation_dir/rendered.json" >/dev/null

if grep -q 'X-Frame-Options' deploy/caddy/ngbot-webapp.Caddyfile; then
	echo "Caddy must not override the Task 1 Telegram Web framing policy" >&2
	exit 1
fi
grep -q 'Content-Security-Policy' deploy/caddy/ngbot-webapp.Caddyfile
grep -q 'Referrer-Policy "no-referrer"' deploy/caddy/ngbot-webapp.Caddyfile
grep -q 'X-Content-Type-Options "nosniff"' deploy/caddy/ngbot-webapp.Caddyfile
grep -q 'reverse_proxy 127.0.0.1:{$NGBOT_WEBAPP_HOST_PORT:18080}' deploy/caddy/ngbot-webapp.Caddyfile
