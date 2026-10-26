#!/usr/bin/env bash
# dev.sh — make-free entry point for the Yunshu API repo.
#
# Why this exists: T01's acceptance left two environment debts on this machine
# (no GNU make; host ports 3306/6379 already taken). Both are solved here so
# that T02 (migrations) can be dispatched without the implementer hitting an
# environment wall mid-card.
#
# It mirrors the Makefile targets one-to-one; neither is a second source of
# truth for behaviour — both call the same underlying commands.
#
#   scripts/dev.sh up | down | ps | env | build | test | vet | lint | check
#   scripts/dev.sh run-gw | run-ctl | run-jobs | migrate
#
# Port handling: if the host port is busy the compose overlay
# deploy/docker-compose.portmap.yml is applied automatically (13306 / 16379)
# and `env` rewrites .env accordingly. The base compose file keeps the
# canonical 3306 / 6379.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_COMPOSE="deploy/docker-compose.yml"
OVERLAY="deploy/docker-compose.portmap.yml"
GO="${GO:-go}"

MYSQL_PORT=3306
REDIS_PORT=6379
USE_OVERLAY=0

port_busy() {
  local p="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${p}\$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ano 2>/dev/null | grep -E "[:.]${p}[[:space:]]" | grep -q LISTENING
  else
    return 1
  fi
}

resolve_ports() {
  if port_busy 3306; then
    MYSQL_PORT=13306
    USE_OVERLAY=1
  fi
  if port_busy 6379; then
    REDIS_PORT=16379
    USE_OVERLAY=1
  fi
}

compose() {
  if [ "$USE_OVERLAY" = 1 ]; then
    docker compose -f "$BASE_COMPOSE" -f "$OVERLAY" "$@"
  else
    docker compose -f "$BASE_COMPOSE" "$@"
  fi
}

report_ports() {
  if [ "$USE_OVERLAY" = 1 ]; then
    echo "note: host port busy, overlay applied -> mysql:${MYSQL_PORT} redis:${REDIS_PORT}" >&2
    echo "note: the base compose file is unchanged (3306 / 6379 stay canonical)" >&2
  fi
}

dsn_mysql() { echo "root:root@tcp(127.0.0.1:${MYSQL_PORT})/yunshu?parseTime=true&loc=UTC&charset=utf8mb4"; }
dsn_log()   { echo "root:root@tcp(127.0.0.1:${MYSQL_PORT})/yunshu_log?parseTime=true&loc=UTC&charset=utf8mb4"; }

write_env() {
  resolve_ports
  report_ports
  if [ -f .env ]; then
    local want="127.0.0.1:${MYSQL_PORT}"
    if ! grep -q "$want" .env; then
      echo "WARN: .env exists but does not point at ${want}. Run: scripts/dev.sh env --force" >&2
      return 0
    fi
    echo ".env already matches resolved ports (${MYSQL_PORT} / ${REDIS_PORT})"
    return 0
  fi
  if [ ! -f .env.example ]; then
    echo "ERROR: .env.example missing" >&2
    exit 1
  fi
  sed -e "s/127\.0\.0\.1:3306/127.0.0.1:${MYSQL_PORT}/g" \
      -e "s/^REDIS_ADDR=127\.0\.0\.1:6379/REDIS_ADDR=127.0.0.1:${REDIS_PORT}/" \
      .env.example > .env
  echo "wrote .env (mysql ${MYSQL_PORT} / redis ${REDIS_PORT})"
}

force_env() {
  resolve_ports
  report_ports
  sed -e "s/127\.0\.0\.1:[0-9]\{4,5\}/127.0.0.1:${MYSQL_PORT}/g" \
      -e "s/^REDIS_ADDR=127\.0\.0\.1:[0-9]\{4,5\}/REDIS_ADDR=127.0.0.1:${REDIS_PORT}/" \
      .env.example > .env
  echo "rewrote .env (mysql ${MYSQL_PORT} / redis ${REDIS_PORT})"
}

# Load .env into the environment for run-* targets, same as the Makefile does.
# An ENV explicitly set by the caller wins over .env: `ENV=production ... migrate
# up` must exercise the production branch (0019 skipped) even though the local
# .env says ENV=dev. Without this, sourcing .env would overwrite the caller.
with_env() {
  local env_override="${ENV-}"
  set -a
  # shellcheck disable=SC1091
  [ -f .env ] && . ./.env
  set +a
  if [ -n "$env_override" ]; then
    export ENV="$env_override"
  fi
  "$@"
}

cmd="${1:-help}"
shift || true

case "$cmd" in
  up)
    resolve_ports; report_ports
    compose up -d --wait --wait-timeout 240
    compose ps
    ;;
  down)
    compose down ;;
  ps)
    resolve_ports
    compose ps ;;
  env)
    if [ "${1:-}" = "--force" ]; then force_env; else write_env; fi
    ;;
  ports)
    resolve_ports
    echo "MYSQL_PORT=${MYSQL_PORT} REDIS_PORT=${REDIS_PORT} OVERLAY=${USE_OVERLAY}"
    ;;
  build) "$GO" build ./... ;;
  test)  "$GO" test ./... ;;
  vet)   "$GO" vet ./... ;;
  lint)
    files="$(gofmt -l $(find . -name '*.go' -not -path './.git/*' -not -path './web/*'))"
    if [ -n "$files" ]; then echo "gofmt required:"; echo "$files"; exit 1; fi
    "$GO" vet ./...
    ;;
  check)
    "$GO" vet ./... && "$GO" test ./...
    if grep -rn '"encoding/json"' --include='*.go' . | grep -v 'internal/jsonx'; then
      echo 'FAIL: encoding/json is only allowed inside internal/jsonx'; exit 1
    fi
    bad="$(grep -rn 'context.Background()' --include='*.go' internal/ cmd/ || true)"
    bad="$(printf '%s\n' "$bad" | grep -v '^cmd/' | grep -v '^$' || true)"
    if [ -n "$bad" ]; then echo 'FAIL: context.Background() outside cmd/'; echo "$bad"; exit 1; fi
    if grep -rn 'QuantumNous' --include='*.go' --include='*.sql' .; then
      echo 'FAIL: forbidden upstream attribution string found'; exit 1
    fi
    echo 'check: grep assertions passed'
    ;;
  migrate)
    if [ -d cmd/migrate ]; then
      with_env "$GO" run ./cmd/migrate "$@"
    else
      echo "no cmd/migrate yet (T01 ships no schema; T02 adds the migration runner)"
    fi
    ;;
  run-gw)   with_env "$GO" run ./cmd/gatewayd ;;
  run-ctl)  with_env "$GO" run ./cmd/controld ;;
  run-jobs) with_env "$GO" run ./cmd/jobsd ;;
  help|*)
    sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
    ;;
esac
