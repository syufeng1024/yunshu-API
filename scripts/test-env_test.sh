#!/usr/bin/env bash
# test-env_test.sh — guard the port precedence in scripts/test-env.sh.
#
# Why this exists: the resolver used to probe 3306 (native install) before
# 13306 (compose overlay). On a machine whose host MySQL is 8.4 that silently
# pointed every package at the wrong baseline — GR-01 pins this project to MySQL
# 8.0 — and produced false-green test runs. The bug was invisible for as long
# as nothing asserted the order, so this file asserts it.
#
# Design notes:
#   * The port prober is stubbed, so no real socket is ever opened. This test
#     must pass on a machine with no containers, no MySQL and no Docker at all.
#   * No temporary files. TMPDIR on the primary dev box is a Windows path, where
#     removing a file trips the safe-delete guard; the target is therefore
#     sourced through process substitution with its auto-invoke tail stripped.
#   * TEST_ENV_TARGET lets you point the assertions at another revision, e.g.
#     TEST_ENV_TARGET=<(...) is not usable, so use a checked-out copy:
#       git show HEAD:scripts/test-env.sh > /tmp/old.sh
#       TEST_ENV_TARGET=/tmp/old.sh bash scripts/test-env_test.sh   # must FAIL
#
# Exit code: 0 when every assertion holds, 1 otherwise.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${TEST_ENV_TARGET:-$ROOT/scripts/test-env.sh}"

if [ ! -f "$TARGET" ]; then
  echo "FAIL: target not found: $TARGET" >&2
  exit 1
fi

checks=0
failures=0

ok() { checks=$((checks + 1)); printf 'PASS  %s\n' "$1"; }
no() { checks=$((checks + 1)); failures=$((failures + 1)); printf 'FAIL  %s\n' "$1"; }

# expect_eq <label> <expected> <actual>
expect_eq() {
  if [ "$2" = "$3" ]; then
    ok "$1"
  else
    no "$1 (expected [$2], got [$3])"
  fi
}

# expect_contains <label> <haystack> <needle>
expect_contains() {
  case "$2" in
    *"$3"*) ok "$1" ;;
    *) no "$1 (missing [$3] in [$2])" ;;
  esac
}

# expect_absent <label> <haystack> <needle>
expect_absent() {
  case "$2" in
    *"$3"*) no "$1 (unexpected [$3] in [$2])" ;;
    *) ok "$1" ;;
  esac
}

# capture <open-ports> [preset-dsn] [preset-redis]
#
# Sources the target with its auto-invoke line removed, stubs the prober so that
# only <open-ports> (a space separated list) is reachable, runs the resolver and
# echoes a machine readable report on stdout:
#
#   <resolver stderr, merged>
#   __RC__=0
#   __DSN__=...
#   __REDIS__=...
#
# The merged stderr is deliberately printed first so a single blob can be
# grepped for warnings. Neither a DSN nor a Redis address can contain the
# sentinel keys, so the blob stays unambiguous.
capture() {
  local open_ports="$1" preset_dsn="${2-}" preset_redis="${3-}"
  local target="$TARGET"
  (
    set +e
    # The invoke tail would resolve before the stub exists; strip it.
    # shellcheck source=/dev/null
    . <(sed '/^_yunshu_test_env ||/d' "$target")

    unset TEST_DB_DSN TEST_REDIS_ADDR
    if [ -n "$preset_dsn" ]; then export TEST_DB_DSN="$preset_dsn"; fi
    if [ -n "$preset_redis" ]; then export TEST_REDIS_ADDR="$preset_redis"; fi

    # Reachability stub — replaces the /dev/tcp probe entirely.
    _yunshu_port_open() {
      case " $open_ports " in
        *" $2 "*) return 0 ;;
        *) return 1 ;;
      esac
    }

    _yunshu_test_env 2>&1
    echo "__RC__=$?"
    echo "__DSN__=${TEST_DB_DSN-}"
    echo "__REDIS__=${TEST_REDIS_ADDR-}"
  )
}

# field <blob> <RC|DSN|REDIS>
field() {
  printf '%s\n' "$1" | sed -n "s/^__${2}__=//p"
}

CONTAINER_PORTS="3306 13306 6379 16379"

printf 'guard: target=%s\n' "$TARGET"
printf 'guard: %s\n' "asserting container-first port precedence"

# ---------------------------------------------------------------------------
# 1. Both MySQL ports reachable -> the container (13306, MySQL 8.0) must win,
#    silently. This is the regression: 3306-first picks the 8.4 host install.
# ---------------------------------------------------------------------------
blob="$(capture "$CONTAINER_PORTS")"
expect_eq     "1.1 both ports open: resolver succeeds"            "0" "$(field "$blob" RC)"
expect_contains "1.2 both ports open: DSN targets 13306"          "$(field "$blob" DSN)" ":13306"
expect_contains "1.3 both ports open: DSN uses container creds"   "$(field "$blob" DSN)" "root:root@tcp(127.0.0.1:13306)"
expect_absent "1.4 both ports open: DSN never targets 3306"       "$(field "$blob" DSN)" ":3306"
expect_absent "1.5 both ports open: no WARNING on stderr"         "$blob" "WARNING"

# ---------------------------------------------------------------------------
# 2. Only the native 3306 is reachable -> fall back to it, but say so loudly on
#    stderr and keep going (rc=0): machines without Docker must still run tests.
# ---------------------------------------------------------------------------
blob="$(capture "3306 16379")"
expect_eq       "2.1 only 3306 open: resolver still succeeds"     "0" "$(field "$blob" RC)"
expect_contains "2.2 only 3306 open: DSN targets 3306"            "$(field "$blob" DSN)" ":3306"
expect_contains "2.3 only 3306 open: DSN uses native creds"       "$(field "$blob" DSN)" "root:123456@tcp(127.0.0.1:3306)"
expect_contains "2.4 only 3306 open: WARNING is printed"          "$blob" "WARNING"
expect_contains "2.5 only 3306 open: warning names the 8.0 vs 8.4 risk" "$blob" "8.4"
expect_contains "2.6 only 3306 open: warning tells how to fix it" "$blob" "docker compose"

# ---------------------------------------------------------------------------
# 3. No MySQL anywhere -> hard error that names the problem, rc=1.
# ---------------------------------------------------------------------------
blob="$(capture "")"
expect_eq       "3.1 no MySQL: resolver fails"                    "1" "$(field "$blob" RC)"
expect_contains "3.2 no MySQL: error text preserved"              "$blob" "no MySQL on"
expect_contains "3.3 no MySQL: error offers the compose command"  "$blob" "docker compose"
expect_eq       "3.4 no MySQL: DSN stays empty"                   "" "$(field "$blob" DSN)"

# ---------------------------------------------------------------------------
# 4. Caller-supplied values always win — no probe, no warning, no rewrite.
# ---------------------------------------------------------------------------
PRESET_DSN='root:pw@tcp(db.internal:3307)/custom?parseTime=true'
PRESET_REDIS='redis.internal:6380'
blob="$(capture "" "$PRESET_DSN" "$PRESET_REDIS")"
expect_eq       "4.1 preset TEST_DB_DSN: resolver succeeds"       "0" "$(field "$blob" RC)"
expect_eq       "4.2 preset TEST_DB_DSN: value untouched"         "$PRESET_DSN" "$(field "$blob" DSN)"
expect_eq       "4.3 preset TEST_REDIS_ADDR: value untouched"     "$PRESET_REDIS" "$(field "$blob" REDIS)"
expect_absent   "4.4 preset values: no probing happened"          "$blob" "no MySQL on"
expect_absent   "4.5 preset values: no WARNING printed"           "$blob" "WARNING"

# ---------------------------------------------------------------------------
# 5. Redis side mirrors MySQL: container 16379 first, native 6379 as fallback.
# ---------------------------------------------------------------------------
blob="$(capture "13306 16379 6379")"
expect_eq       "5.1 redis: container 16379 wins over 6379"       "127.0.0.1:16379" "$(field "$blob" REDIS)"
blob="$(capture "13306 6379")"
expect_eq       "5.2 redis: falls back to 6379"                   "127.0.0.1:6379" "$(field "$blob" REDIS)"
blob="$(capture "13306")"
expect_eq       "5.3 redis: absent everywhere fails"              "1" "$(field "$blob" RC)"
expect_contains "5.4 redis: error names the redis ports"          "$blob" "no Redis on"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf 'guard: %d checks, %d failure(s)\n' "$checks" "$failures"
if [ "$failures" -ne 0 ]; then
  printf 'guard: FAIL — %s no longer prefers the container port pair\n' "$TARGET" >&2
  exit 1
fi
printf 'guard: OK — %s prefers 13306 / 16379 and warns on the 3306 fallback\n' "$TARGET"
exit 0
