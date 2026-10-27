#!/usr/bin/env bash
# test-env.sh — export the test DSN/Redis address used by every package's
# TestMain. Source it; do not execute it.
#
# Why a resolver: the repository's tests default to 127.0.0.1:13306 (the
# compose overlay), while a developer machine may run MySQL natively on 3306.
# `scripts/dev.sh ci` must be green on both without requiring a manual export.
#
# Precedence:
#   1. TEST_DB_DSN / TEST_REDIS_ADDR already set by the caller — never overridden.
#   2. Port 3306 reachable -> root:123456 (the local native default).
#   3. Port 13306 reachable -> root:root (deploy/docker-compose.yml).
# The credentials are local test values, not production secrets; they are the
# same ones already hard-coded as defaults in the package TestMains.
#
# The database name is yunshu_test. Tests create it if absent.

_yunshu_port_open() {
  local host="$1" port="$2"
  (exec 3<>"/dev/tcp/${host}/${port}") >/dev/null 2>&1
}

_yunshu_test_db_dsn() {
  local port user pass
  if _yunshu_port_open 127.0.0.1 3306; then
    port=3306; user=root; pass=123456
  elif _yunshu_port_open 127.0.0.1 13306; then
    port=13306; user=root; pass=root
  else
    echo "test-env: no MySQL on 127.0.0.1:3306 or :13306" >&2
    return 1
  fi
  printf 'root:%s@tcp(127.0.0.1:%s)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true' \
    "$pass" "$port"
}

_yunshu_test_env() {
  if [ -z "${TEST_DB_DSN:-}" ]; then
    local dsn
    dsn="$(_yunshu_test_db_dsn)" || return 1
    export TEST_DB_DSN="$dsn"
  fi

  if [ -z "${TEST_REDIS_ADDR:-}" ]; then
    if _yunshu_port_open 127.0.0.1 16379; then
      export TEST_REDIS_ADDR="127.0.0.1:16379"
    elif _yunshu_port_open 127.0.0.1 6379; then
      export TEST_REDIS_ADDR="127.0.0.1:6379"
    else
      echo "test-env: no Redis on 127.0.0.1:16379 or :6379" >&2
      return 1
    fi
  fi

  # Test binaries pick their own logical DB from their package directory; an
  # explicit TEST_REDIS_DB still wins.
  export TEST_DB_DSN TEST_REDIS_ADDR
}

_yunshu_test_env || return 1 2>/dev/null || exit 1
