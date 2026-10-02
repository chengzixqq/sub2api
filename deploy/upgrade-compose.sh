#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  echo "Usage: $0 <candidate-image> <backup-root>" >&2
  echo "Example: $0 ghcr.io/owner/sub2api:0.2.11 /var/backups/sub2api" >&2
}

if [[ $# -ne 2 || -z ${1//[[:space:]]/} || $1 == -* ]]; then
  usage
  exit 2
fi

candidate_image=$1
backup_root=$2
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd "$script_dir"

health_timeout=${SUB2API_HEALTH_TIMEOUT_SECONDS:-240}
if [[ ! $health_timeout =~ ^[0-9]+$ ]] || (( health_timeout < 1 )); then
  echo "SUB2API_HEALTH_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 2
fi

if ! command -v docker >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1 || ! command -v sha256sum >/dev/null 2>&1; then
  echo "docker, python3, and sha256sum are required" >&2
  exit 2
fi

compose() {
  docker compose "$@"
}

wait_for_healthy() {
  local timeout=$1
  local started=$SECONDS
  local container_id health

  while (( SECONDS - started < timeout )); do
    container_id=$(compose ps -q sub2api 2>/dev/null || true)
    if [[ -n $container_id ]]; then
      health=$(docker inspect --format '{{.State.Health.Status}}' "$container_id" 2>/dev/null || true)
      case "$health" in
        healthy) return 0 ;;
        unhealthy) return 1 ;;
      esac
    fi
    sleep 2
  done
  return 1
}

check_dependencies() {
  compose exec -T postgres sh -ec 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" && test "$(psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atqc "SELECT 1")" = 1' >/dev/null
  [[ $(compose exec -T redis redis-cli ping | tr -d '\r') == PONG ]]
}

mapfile -t app_containers < <(compose ps -q sub2api)
if (( ${#app_containers[@]} != 1 )); then
  echo "Expected exactly one running sub2api container" >&2
  exit 1
fi
old_container=${app_containers[0]}
old_health=$(docker inspect --format '{{.State.Health.Status}}' "$old_container")
if [[ $old_health != healthy ]]; then
  echo "Current sub2api container is not healthy (status: ${old_health:-unknown})" >&2
  exit 1
fi
check_dependencies
old_image_ref=$(docker inspect --format '{{.Config.Image}}' "$old_container")
old_image_id=$(docker inspect --format '{{.Image}}' "$old_container")

echo "Pulling candidate image: $candidate_image"
SUB2API_IMAGE="$candidate_image" compose pull sub2api
candidate_image_id=$(docker image inspect --format '{{.Id}}' "$candidate_image")
candidate_pinned_image="sub2api-candidate:$$-$RANDOM"
docker image tag "$candidate_image_id" "$candidate_pinned_image"
if ! resolved_candidate_image=$(SUB2API_IMAGE="$candidate_pinned_image" compose config --format json \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["sub2api"]["image"])'); then
  echo "Could not resolve sub2api image from the Compose configuration" >&2
  exit 1
fi
if [[ $resolved_candidate_image != "$candidate_pinned_image" ]]; then
  echo "Compose service sub2api does not use SUB2API_IMAGE; refusing an unpinned upgrade" >&2
  exit 1
fi

echo "Checking migration compatibility against the live database"
if ! SUB2API_IMAGE="$candidate_pinned_image" compose run --rm --no-deps --pull never -T \
  --name "sub2api-preflight-$$-$RANDOM" \
  --entrypoint /app/migration-preflight sub2api --require-no-pending; then
  echo "Migration preflight failed; the running service was not changed" >&2
  exit 1
fi

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_root_existed=0
if [[ -e $backup_root && ! -d $backup_root ]]; then
  echo "Backup root exists and is not a directory: $backup_root" >&2
  exit 1
fi
if [[ -d $backup_root ]]; then
  backup_root_existed=1
fi
mkdir -p -- "$backup_root"
if (( ! backup_root_existed )); then
  chmod 700 "$backup_root"
fi
backup_dir=$(mktemp -d "$backup_root/sub2api-upgrade-${timestamp}.XXXXXX")
chmod 700 "$backup_dir"
mkdir -p "$backup_dir/app-data" "$backup_dir/compose-inputs"

rollback_image="sub2api-rollback:${timestamp}-$$"
docker image tag "$old_image_id" "$rollback_image"

backup_state=backup_in_progress
write_metadata() {
  python3 - "$backup_dir" "$backup_state" "$old_image_ref" "$old_image_id" \
    "$candidate_image" "$candidate_image_id" "$candidate_pinned_image" "$rollback_image" \
    "${COMPOSE_FILE:-docker-compose.yml}" <<'PY'
import hashlib
import json
import pathlib
import sys
from datetime import datetime, timezone

root = pathlib.Path(sys.argv[1])
files = []
for path in sorted(root.rglob("*")):
    if path.is_file() and path.name not in {"metadata.json", "SHA256SUMS"}:
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        files.append({"path": str(path.relative_to(root)), "sha256": digest})

metadata = {
    "created_at": datetime.now(timezone.utc).isoformat(),
    "state": sys.argv[2],
    "project_directory": str(pathlib.Path.cwd()),
    "compose_file": sys.argv[9],
    "schema_gate": {"require_no_pending": True, "pending_migrations": 0},
    "previous_image": {"reference": sys.argv[3], "image_id": sys.argv[4]},
    "candidate_image": {
        "reference": sys.argv[5],
        "image_id": sys.argv[6],
        "pinned_reference": sys.argv[7],
    },
    "rollback_image": sys.argv[8],
    "backups": files,
}
(root / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
PY
  chmod 600 "$backup_dir/metadata.json"
}

persist_image_reference() {
  python3 - "$script_dir/.env" "$1" <<'PY'
import os
import pathlib
import re
import sys
import tempfile

path = pathlib.Path(sys.argv[1])
value = sys.argv[2]
if path.exists():
    text = path.read_text(encoding="utf-8")
    mode = path.stat().st_mode & 0o777
else:
    text = ""
    mode = 0o600

lines = text.splitlines()
pattern = re.compile(r"^\s*(?:export\s+)?SUB2API_IMAGE\s*=")
replacement = f"SUB2API_IMAGE={value}"
updated = False
result = []
for line in lines:
    if pattern.match(line):
        if not updated:
            result.append(replacement)
            updated = True
    else:
        result.append(line)
if not updated:
    result.append(replacement)

content = "\n".join(result).rstrip("\n") + "\n"
fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
try:
    with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as output:
        output.write(content)
    os.chmod(temporary, mode)
    os.replace(temporary, path)
except BaseException:
    try:
        os.unlink(temporary)
    except FileNotFoundError:
        pass
    raise
PY
}

rollback_previous_image() {
  backup_state=rollback_failed
  local config_restore_failed=0
  if ! persist_image_reference "$rollback_image"; then
    config_restore_failed=1
  fi
  if ! SUB2API_IMAGE="$rollback_image" compose up -d --no-deps --force-recreate --pull never sub2api; then
    backup_state=rollback_failed
    return 1
  fi
  if wait_for_healthy "$health_timeout" && check_dependencies; then
    if (( config_restore_failed )); then
      backup_state=rollback_config_restore_failed
      return 1
    fi
    backup_state=rolled_back
    return 0
  fi
  backup_state=rollback_unhealthy
  return 1
}

on_error() {
  local status=$?
  trap - ERR
  if (( switch_started )); then
    if rollback_previous_image; then
      echo "The previous image was restored after an upgrade error" >&2
    else
      echo "Automatic rollback was incomplete; inspect the recovery bundle" >&2
    fi
  else
    backup_state=failed_before_switch
  fi
  write_metadata || true
  echo "Upgrade stopped with status $status. Recovery bundle: $backup_dir" >&2
  exit "$status"
}
switch_started=0
trap on_error ERR

# Keep the effective Compose configuration and its source inputs with the backup.
SUB2API_IMAGE="$candidate_pinned_image" compose config --format json > "$backup_dir/compose-resolved.json"
if [[ -f .env ]]; then
  cp -p -- .env "$backup_dir/.env"
fi
compose_files=${COMPOSE_FILE:-docker-compose.yml}
IFS=: read -r -a compose_file_list <<< "$compose_files"
for index in "${!compose_file_list[@]}"; do
  compose_path=${compose_file_list[$index]}
  [[ $compose_path == /* ]] || compose_path="$script_dir/$compose_path"
  cp -p -- "$compose_path" "$backup_dir/compose-inputs/$(printf '%02d' "$index")-$(basename -- "$compose_path")"
done

docker cp "$old_container:/app/data/." "$backup_dir/app-data/"
compose exec -T postgres sh -ec 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' > "$backup_dir/postgres.dump"
compose exec -T postgres pg_restore --list - < "$backup_dir/postgres.dump" > /dev/null

redis_container=$(compose ps -q redis)
redis_tmp="/tmp/sub2api-redis-backup-${timestamp}-$$.rdb"
compose exec -T redis redis-cli --rdb "$redis_tmp"
docker cp "$redis_container:$redis_tmp" "$backup_dir/redis.rdb"
compose exec -T redis rm -f "$redis_tmp"

chmod 600 "$backup_dir"/compose-resolved.json "$backup_dir"/postgres.dump "$backup_dir"/redis.rdb
(cd "$backup_dir" && find . -type f ! -name metadata.json ! -name SHA256SUMS -print0 \
  | sort -z \
  | xargs -0 sha256sum > SHA256SUMS)
chmod 600 "$backup_dir/SHA256SUMS"
backup_state=backed_up
write_metadata

echo "Starting candidate image; recovery bundle: $backup_dir"
switch_started=1
if ! SUB2API_IMAGE="$candidate_pinned_image" compose up -d --no-deps --force-recreate --pull never sub2api; then
  echo "Candidate start failed; restoring the previous image" >&2
elif wait_for_healthy "$health_timeout"; then
  if ! check_dependencies; then
    echo "Candidate is healthy but PostgreSQL or Redis failed its health check" >&2
  elif ! persist_image_reference "$candidate_image"; then
    echo "Candidate is healthy but failed to persist SUB2API_IMAGE; restoring the previous image" >&2
  else
    backup_state=healthy
    write_metadata
    switch_started=0
    trap - ERR
    echo "Upgrade completed and health check passed. Recovery bundle: $backup_dir"
    exit 0
  fi
else
  echo "Candidate health check failed; restoring the previous image" >&2
fi

if rollback_previous_image; then
  write_metadata
  trap - ERR
  echo "Previous image restored and healthy. Recovery bundle: $backup_dir" >&2
else
  write_metadata || true
  trap - ERR
  echo "Automatic image rollback failed. Preserve data and use recovery bundle: $backup_dir" >&2
fi
exit 1
