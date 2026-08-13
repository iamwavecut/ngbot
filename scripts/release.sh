#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_root"

require_command() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "$1 is required" >&2
		exit 1
	}
}

require_sqlite_tool() {
	command -v sqlite3 >/dev/null 2>&1 && return
	require_command python3
}

verify_snapshot() {
	database=$1
	if command -v sqlite3 >/dev/null 2>&1; then
		quick_check=$(sqlite3 "$database" "PRAGMA quick_check;")
		[ "$quick_check" = "ok" ] || {
			echo "SQLite quick_check failed for $database: $quick_check" >&2
			exit 1
		}
		foreign_keys=$(sqlite3 "$database" "PRAGMA foreign_key_check;")
		[ -z "$foreign_keys" ] || {
			echo "SQLite foreign_key_check failed for $database" >&2
			exit 1
		}
		return
	fi
	python3 - "$database" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
quick_check = connection.execute("PRAGMA quick_check").fetchall()
foreign_keys = connection.execute("PRAGMA foreign_key_check").fetchall()
connection.close()
if quick_check != [("ok",)]:
    raise SystemExit(f"SQLite quick_check failed for {sys.argv[1]}: {quick_check}")
if foreign_keys:
    raise SystemExit(f"SQLite foreign_key_check failed for {sys.argv[1]}")
PY
}

snapshot_database() {
	source_database=$1
	target_database=$2
	umask 077
	if command -v sqlite3 >/dev/null 2>&1; then
		sqlite3 "$source_database" ".backup '$target_database'"
	else
		python3 - "$source_database" "$target_database" <<'PY'
import sqlite3
import sys

source = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
target = sqlite3.connect(sys.argv[2])
source.backup(target)
target.close()
source.close()
PY
	fi
	chmod 0600 "$target_database"
	verify_snapshot "$target_database"
}

wait_for_health() {
	container_id=$1
	elapsed=0
	timeout=${NGBOT_HEALTH_TIMEOUT_SECONDS:-120}
	while [ "$elapsed" -lt "$timeout" ]; do
		status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$container_id")
		[ "$status" = "healthy" ] && return 0
		[ "$status" = "unhealthy" ] && break
		sleep 2
		elapsed=$((elapsed + 2))
	done
	echo "ngbot did not become healthy" >&2
	docker compose ps >&2
	exit 1
}

verify_runtime() {
	container_id=$(docker compose ps -q ngbot)
	[ -n "$container_id" ] || {
		echo "ngbot container is not running" >&2
		exit 1
	}
	wait_for_health "$container_id"
	running_image_id=$(docker inspect --format '{{.Image}}' "$container_id")
	expected_image_id=$(docker image inspect "$NGBOT_IMAGE" --format '{{.Id}}')
	[ "$running_image_id" = "$expected_image_id" ] || {
		echo "running image $running_image_id does not match $expected_image_id" >&2
		exit 1
	}
	[ "$(docker inspect --format '{{.RestartCount}}' "$container_id")" = "0" ]
	[ "$(docker inspect --format '{{.State.OOMKilled}}' "$container_id")" = "false" ]
	curl --fail --silent --show-error "http://127.0.0.1:${NGBOT_WEBAPP_HOST_PORT:-18080}/livez" >/dev/null
	curl --fail --silent --show-error "http://127.0.0.1:${NGBOT_WEBAPP_HOST_PORT:-18080}/readyz" >/dev/null
	curl --fail --silent --show-error "${NGBOT_PUBLIC_URL%/}/livez" >/dev/null
	curl --fail --silent --show-error "${NGBOT_PUBLIC_URL%/}/readyz" >/dev/null
}

release() {
	for command in caddy curl docker git; do
		require_command "$command"
	done
	require_sqlite_tool
	: "${NGBOT_VERSION:?set NGBOT_VERSION to the release version}"
	: "${NGBOT_PUBLIC_URL:?set NGBOT_PUBLIC_URL to the public HTTPS origin}"
	: "${NGBOT_CADDYFILE:?set NGBOT_CADDYFILE to the active Caddy configuration}"
	: "${NGBOT_BACKUP_DIR:?set NGBOT_BACKUP_DIR to a secured backup directory}"
	: "${NGBOT_DATA_PATH:?set NGBOT_DATA_PATH to the secured host data directory}"
	case "$NGBOT_PUBLIC_URL" in
		https://*) ;;
		*) echo "NGBOT_PUBLIC_URL must use HTTPS" >&2; exit 1 ;;
	esac

	release_revision=$(git rev-parse HEAD)
	release_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	release_stamp=$(date -u +%Y%m%dT%H%M%SZ)
	export NGBOT_REVISION=$release_revision
	export NGBOT_BUILD_DATE=$release_date
	export NGBOT_IMAGE="ngbot:$release_revision"
	install -d -m 0700 "$NGBOT_BACKUP_DIR"

	state_file="$NGBOT_BACKUP_DIR/release-$release_stamp.state"
	umask 077
	previous_container=$(docker compose ps -q ngbot)
	if [ -n "$previous_container" ]; then
		previous_image_id=$(docker inspect --format '{{.Image}}' "$previous_container")
		previous_image="ngbot:rollback-$release_stamp"
		docker image tag "$previous_image_id" "$previous_image"
		printf 'PREVIOUS_IMAGE=%s\nPREVIOUS_IMAGE_ID=%s\n' "$previous_image" "$previous_image_id" >"$state_file"
	fi

	database="$NGBOT_DATA_PATH/bot.db"
	if [ -f "$database" ]; then
		snapshot_database "$database" "$NGBOT_BACKUP_DIR/bot-$release_stamp-online.db"
	fi

	docker compose build ngbot
	image_id=$(docker image inspect "$NGBOT_IMAGE" --format '{{.Id}}')
	[ "$(docker image inspect "$NGBOT_IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.version"}}')" = "$NGBOT_VERSION" ]
	[ "$(docker image inspect "$NGBOT_IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')" = "$release_revision" ]
	[ "$(docker image inspect "$NGBOT_IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.created"}}')" = "$release_date" ]
	version_output=$(docker run --rm --entrypoint ./ngbot "$NGBOT_IMAGE" --version)
	[ "$version_output" = "ngbot version=$NGBOT_VERSION revision=$release_revision build_date=$release_date" ]
	printf 'RELEASE_IMAGE=%s\nRELEASE_IMAGE_ID=%s\nRELEASE_REVISION=%s\n' "$NGBOT_IMAGE" "$image_id" "$release_revision" >>"$state_file"

	caddy validate --config "$NGBOT_CADDYFILE"
	docker compose stop ngbot
	if [ -f "$database" ]; then
		snapshot_database "$database" "$NGBOT_BACKUP_DIR/bot-$release_stamp-offline.db"
	fi
	docker compose run --rm --no-deps ngbot --database-maintenance
	docker compose up -d --no-build ngbot
	caddy reload --config "$NGBOT_CADDYFILE"
	verify_runtime

	soak_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	sleep "${NGBOT_SOAK_SECONDS:-30}"
	verify_runtime
	if docker compose logs --since "$soak_started" ngbot 2>&1 | grep -Eiq 'panic|fatal|out of memory|oom|database is locked'; then
		echo "fatal, OOM, or database-lock evidence found during release soak" >&2
		exit 1
	fi
	echo "release verified: revision=$release_revision image=$NGBOT_IMAGE image_id=$image_id state=$state_file"
}

rollback() {
	require_command docker
	: "${NGBOT_ROLLBACK_STATE:?set NGBOT_ROLLBACK_STATE to a release state file}"
	: "${NGBOT_SCHEMA_COMPATIBLE:?set NGBOT_SCHEMA_COMPATIBLE=yes only after checking schema compatibility}"
	[ "$NGBOT_SCHEMA_COMPATIBLE" = "yes" ] || {
		echo "rollback refused: older binaries may be incompatible with the migrated schema" >&2
		exit 1
	}
	[ -f "$NGBOT_ROLLBACK_STATE" ] || {
		echo "rollback state file is missing" >&2
		exit 1
	}
	. "$NGBOT_ROLLBACK_STATE"
	: "${PREVIOUS_IMAGE:?rollback state has no previous image}"
	export NGBOT_IMAGE=$PREVIOUS_IMAGE
	docker compose up -d --no-build --force-recreate ngbot
	verify_runtime
	echo "rollback verified: image=$PREVIOUS_IMAGE"
}

case "${1:-}" in
	release) release ;;
	rollback) rollback ;;
	*) echo "usage: $0 release|rollback" >&2; exit 2 ;;
esac
