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
#   2. Container stack port: 13306 (MySQL) / 16379 (Redis) — root:root / —.
#   3. Native local install as a fallback: 3306 (MySQL) / 6379 (Redis).
# The container port is probed FIRST on both sides, and deliberately so: GR-01
# pins this project to MySQL 8.0 and deploy/docker-compose.yml provisions
# exactly that version, whereas a native MySQL on 3306 is commonly 8.4 — a
# different baseline that produces false-green test runs. The 3306 fallback is
# kept (machines without Docker must still be able to run tests) but it warns
# loudly on stderr. deploy/docker-compose.portmap.yml documents the same rule.
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
  # Container stack (MySQL 8.0) first: it is the baseline GR-01 pins.
  if _yunshu_port_open 127.0.0.1 13306; then
    port=13306; user=root; pass=root
  elif _yunshu_port_open 127.0.0.1 3306; then
    port=3306; user=root; pass=123456
    # Warn, do not block: a machine without Docker still has to run the tests.
    echo "test-env: WARNING: falling back to the native MySQL on 127.0.0.1:3306." >&2
    echo "test-env: WARNING: that server is likely MySQL 8.4, but GR-01 pins this" >&2
    echo "test-env: WARNING: project to MySQL 8.0 — results may be false green." >&2
    echo "test-env: WARNING: for acceptance/delivery, start the container stack and retry:" >&2
    echo "test-env:   docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.portmap.yml up -d --wait" >&2
  else
    echo "test-env: no MySQL on 127.0.0.1:3306 or :13306" >&2
    echo "test-env: start the container stack first:" >&2
    echo "test-env:   docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.portmap.yml up -d --wait" >&2
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

# The line below is deliberately the only auto-invocation. Its prefix is also
# the sentinel scripts/test-env_test.sh greps for (sed '/^_yunshu_test_env ||/d')
# to source the functions without running the resolver, so keep the prefix if
# you edit it — otherwise the guard silently stops stubbing the port prober.
_yunshu_test_env || return 1 2>/dev/null || exit 1
