#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
temporary_root=$(mktemp -d)
trap 'rm -rf -- "$temporary_root"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

setup_case() {
  local name=$1
  case_root="$temporary_root/$name"
  mkdir -p "$case_root/deploy" "$case_root/bin"
  cp "$repo_root/deploy/upgrade-compose.sh" "$case_root/deploy/upgrade-compose.sh"
  cp "$repo_root/deploy/docker-compose.yml" "$case_root/deploy/docker-compose.yml"
  printf 'POSTGRES_PASSWORD=test-secret\nSUB2API_IMAGE=old-image:0.2.8\n' > "$case_root/deploy/.env"
  chmod 600 "$case_root/deploy/.env"
  cat > "$case_root/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail

state=$MOCK_STATE
printf 'image=%s %s\n' "${SUB2API_IMAGE:-}" "$*" >> "$MOCK_DOCKER_LOG"

if [[ ${1:-} == compose ]]; then
  shift
  command=${1:-}
  shift || true
  case "$command" in
    config)
      printf '{"services":{"sub2api":{"image":"%s"}}}\n' "${SUB2API_IMAGE:-}"
      ;;
    pull)
      ;;
    ps)
      if [[ ${2:-} == sub2api ]]; then
        printf 'app-container\n'
      elif [[ ${2:-} == redis ]]; then
        printf 'redis-container\n'
      fi
      ;;
    exec)
      [[ ${1:-} == -T ]] && shift
      service=${1:-}
      shift || true
      args="$*"
      if [[ $service == postgres ]]; then
        if [[ $args == *pg_dump* ]]; then
          printf 'fixture-postgres-dump\n'
        elif [[ $args == *pg_restore* ]]; then
          dump=$(cat)
          [[ $dump == fixture-postgres-dump* ]] || exit 22
          printf 'fixture archive\n'
        fi
      elif [[ $service == redis ]]; then
        if [[ $args == *'redis-cli ping'* ]]; then
          printf 'PONG\n'
        elif [[ $args == *'redis-cli --rdb'* ]]; then
          printf 'fixture-redis-rdb\n' > "$state/redis-working.rdb"
        fi
      fi
      ;;
    run)
      if [[ ${MOCK_PREFLIGHT_FAIL:-0} == 1 ]]; then
        exit 42
      fi
      ;;
    up)
      [[ -n ${SUB2API_IMAGE:-} ]] || exit 23
      printf '%s' "$SUB2API_IMAGE" > "$state/current-image"
      ;;
    *)
      echo "unexpected docker compose command: $command $*" >&2
      exit 24
      ;;
  esac
  exit 0
fi

case "${1:-}" in
  inspect)
    if [[ ${2:-} == --format ]]; then
      format=$3
      container=$4
      current=$(<"$state/current-image")
      case "$format" in
        *State.Health.Status*)
          if [[ $current == sub2api-candidate:* && ${MOCK_CANDIDATE_HEALTH:-healthy} != healthy ]]; then
            printf '%s\n' "$MOCK_CANDIDATE_HEALTH"
          else
            printf 'healthy\n'
          fi
          ;;
        *Config.Image*)
          printf '%s\n' "$current"
          ;;
        *.Image*)
          if [[ $current == old-image:* || $current == sub2api-rollback:* ]]; then
            printf 'sha256:old\n'
          else
            printf 'sha256:candidate\n'
          fi
          ;;
        *)
          echo "unexpected docker inspect format: $format ($container)" >&2
          exit 25
          ;;
      esac
    else
      printf 'sha256:candidate\n'
    fi
    ;;
  image)
    case "${2:-}" in
      inspect) printf 'sha256:candidate\n' ;;
      tag) ;;
      *) exit 26 ;;
    esac
    ;;
  cp)
    source=$2
    destination=$3
    if [[ $source == app-container:/app/data/. ]]; then
      mkdir -p "$destination"
      printf 'fixture app data\n' > "$destination/config.yaml"
    elif [[ $source == redis-container:* ]]; then
      cp "$state/redis-working.rdb" "$destination"
    else
      echo "unexpected docker cp source: $source" >&2
      exit 27
    fi
    ;;
  *)
    echo "unexpected docker command: $*" >&2
    exit 28
    ;;
esac
MOCK
  chmod +x "$case_root/bin/docker"
  printf 'old-image:0.2.8\n' > "$case_root/current-image"
  : > "$case_root/docker.log"
}

run_upgrade() {
  local backup_root=$1
  local case_root=$2
  shift 2
  UPGRADE_STATUS=0
  if env \
    PATH="$case_root/bin:$PATH" \
    MOCK_STATE="$case_root" \
    MOCK_DOCKER_LOG="$case_root/docker.log" \
    MOCK_REAL_PYTHON3="$(command -v python3)" \
    SUB2API_HEALTH_TIMEOUT_SECONDS=5 \
    "$@" \
    bash "$case_root/deploy/upgrade-compose.sh" \
    registry.example/sub2api:0.2.11 "$backup_root" > "$case_root/output.log" 2>&1; then
    UPGRADE_STATUS=0
  else
    UPGRADE_STATUS=$?
  fi
}

test_preflight_failure_stops_before_backup_or_switch() {
  setup_case preflight-failure
  local backup_root="$case_root/backups"
  run_upgrade "$backup_root" "$case_root" MOCK_PREFLIGHT_FAIL=1
  [[ $UPGRADE_STATUS -ne 0 ]] || fail "preflight failure unexpectedly succeeded"
  [[ ! -e $backup_root ]] || fail "preflight failure created a backup directory"
  grep -q 'compose run' "$case_root/docker.log" || fail "preflight was not invoked"
  ! grep -q 'compose up' "$case_root/docker.log" || fail "service switched after preflight failure"
  [[ $(<"$case_root/current-image") == old-image:0.2.8 ]] || fail "old image was not preserved"
}

test_success_backs_up_and_pins_one_candidate_image() {
  setup_case successful-upgrade
  local backup_root="$case_root/backups"
  run_upgrade "$backup_root" "$case_root"
  [[ $UPGRADE_STATUS -eq 0 ]] || { cat "$case_root/output.log" >&2; fail "successful upgrade failed"; }

  local bundle metadata
  bundle=$(find "$backup_root" -mindepth 1 -maxdepth 1 -type d -name 'sub2api-upgrade-*' -print -quit)
  [[ -n $bundle ]] || fail "recovery bundle was not created"
  [[ -f $bundle/postgres.dump && -f $bundle/redis.rdb && -f $bundle/app-data/config.yaml ]] || fail "data backups are incomplete"
  [[ -f $bundle/compose-resolved.json && -f $bundle/compose-inputs/00-docker-compose.yml && -f $bundle/.env ]] || fail "deployment inputs are incomplete"
  (cd "$bundle" && sha256sum -c SHA256SUMS >/dev/null) || fail "backup checksum verification failed"
  grep -q '^SUB2API_IMAGE=registry.example/sub2api:0.2.11$' "$case_root/deploy/.env" || fail "candidate image was not persisted in .env"
  grep -q '^POSTGRES_PASSWORD=test-secret$' "$case_root/deploy/.env" || fail "unrelated .env content was lost"

  local preflight_image switch_image
  preflight_image=$(sed -n 's/^image=\([^ ]*\) compose run .*/\1/p' "$case_root/docker.log")
  switch_image=$(sed -n 's/^image=\([^ ]*\) compose up .*sub2api$/\1/p' "$case_root/docker.log" | head -1)
  [[ -n $preflight_image && $preflight_image == "$switch_image" ]] || fail "preflight and switch did not use the same pinned image"
  metadata="$bundle/metadata.json"
  python3 - "$metadata" <<'PY'
import json
import sys
metadata = json.load(open(sys.argv[1], encoding="utf-8"))
assert metadata["state"] == "healthy", metadata
assert metadata["candidate_image"]["image_id"] == "sha256:candidate", metadata
assert metadata["candidate_image"]["pinned_reference"].startswith("sub2api-candidate:"), metadata
assert metadata["schema_gate"]["pending_migrations"] == 0, metadata
PY
}

test_existing_backup_root_permissions_are_preserved() {
  setup_case existing-backup-root
  local backup_root="$case_root/backups"
  mkdir -p "$backup_root"
  chmod 755 "$backup_root"
  local original_mode
  original_mode=$(stat -c '%a' "$backup_root")

  run_upgrade "$backup_root" "$case_root"
  [[ $UPGRADE_STATUS -eq 0 ]] || { cat "$case_root/output.log" >&2; fail "upgrade with existing backup root failed"; }
  [[ $(stat -c '%a' "$backup_root") == "$original_mode" ]] || fail "existing backup root permissions changed"
}

test_unhealthy_candidate_pins_rollback_image_and_preserves_other_env() {
  setup_case failed-health
  local backup_root="$case_root/backups"
  sed -i '/^SUB2API_IMAGE=/d' "$case_root/deploy/.env"
  run_upgrade "$backup_root" "$case_root" MOCK_CANDIDATE_HEALTH=unhealthy
  [[ $UPGRADE_STATUS -ne 0 ]] || fail "unhealthy candidate unexpectedly succeeded"
  local rollback_ref
  rollback_ref=$(sed -n 's/^SUB2API_IMAGE=//p' "$case_root/deploy/.env")
  [[ $rollback_ref == sub2api-rollback:* ]] || fail "rollback image was not persisted for future Compose operations"
  [[ $(<"$case_root/current-image") == "$rollback_ref" ]] || fail "configured rollback image does not match the running container"
  grep -q '^POSTGRES_PASSWORD=test-secret$' "$case_root/deploy/.env" || fail "unrelated .env content was lost during rollback"
  local metadata
  metadata=$(find "$backup_root" -name metadata.json -print -quit)
  python3 - "$metadata" <<'PY'
import json
import sys
metadata = json.load(open(sys.argv[1], encoding="utf-8"))
assert metadata["state"] == "rolled_back", metadata
PY
}

test_metadata_failure_after_switch_rolls_back_image_and_config() {
  setup_case metadata-failure
  local backup_root="$case_root/backups"
  cat > "$case_root/bin/python3" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${MOCK_FAIL_HEALTHY_METADATA:-0} == 1 && ${1:-} == - && ${3:-} == healthy ]]; then
  cat >/dev/null
  exit 73
fi
exec "$MOCK_REAL_PYTHON3" "$@"
MOCK
  chmod +x "$case_root/bin/python3"

  run_upgrade "$backup_root" "$case_root" MOCK_FAIL_HEALTHY_METADATA=1
  [[ $UPGRADE_STATUS -ne 0 ]] || fail "metadata failure unexpectedly succeeded"
  local rollback_ref
  rollback_ref=$(sed -n 's/^SUB2API_IMAGE=//p' "$case_root/deploy/.env")
  [[ $rollback_ref == sub2api-rollback:* ]] || fail "metadata failure did not persist the rollback image"
  [[ $(<"$case_root/current-image") == "$rollback_ref" ]] || fail "metadata failure left the candidate image running"
  local metadata
  metadata=$(find "$backup_root" -name metadata.json -print -quit)
  python3 - "$metadata" <<'PY'
import json
import sys
metadata = json.load(open(sys.argv[1], encoding="utf-8"))
assert metadata["state"] == "rolled_back", metadata
PY
}

test_preflight_failure_stops_before_backup_or_switch
test_success_backs_up_and_pins_one_candidate_image
test_existing_backup_root_permissions_are_preserved
test_unhealthy_candidate_pins_rollback_image_and_preserves_other_env
test_metadata_failure_after_switch_rolls_back_image_and_config
echo "upgrade-compose tests passed"
