#!/usr/bin/env bash
# test-gwt.sh — T21 deliverable 1: the G-01..G-12 entry point.
#
# The acceptance package owns the real-MySQL/real-Redis fixtures; this script
# only supplies the environment and a stable -run selector.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# shellcheck source=scripts/test-env.sh
. "$ROOT/scripts/test-env.sh"

exec go test -count=1 -v -run 'TestGWT' ./internal/acceptance/ "$@"
