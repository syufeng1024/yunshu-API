#!/usr/bin/env bash
# Guard for files whose exact bytes are hashed or compiled into the binary.
#
# The migration engine records a SHA-256 per applied migration and refuses to
# run when the bytes change (internal/platform/migrate/load.go). A Windows
# checkout with core.autocrlf=true rewrites every line ending to CRLF, which
# changes all of those hashes: `migrate up` fails against a database that was
# provisioned from an LF checkout, and an image built here stops matching a CI
# image built from the same commit. .gitattributes pins these paths to LF, but
# that does not renormalise a working tree that was checked out before the rule
# existed.
#
# Deliberately dependency-free (bash + tr/wc only) so `scripts/dev.sh ci` works
# on a machine with nothing but Go installed — same reason as lint.sh.
#
# Paths reach the loop through `while IFS= read -r`, never as one batched
# argument list: on this host such a call fails with "environment is too large
# for exec" and returns an empty result, which would silently turn the guard
# into a pass.
#
# The Lua scripts under internal/*/scripts are embedded too but are never
# hashed, so they are pinned in .gitattributes and left out of this mandatory
# list (T26 §8): enforcing them here would fail a fresh clone on historical
# files for no correctness gain.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

checked=0
hits=""

# Only add paths whose bytes are hashed, or embedded without any normalisation
# on the read path. Text that is merely shown to a human does not belong here.
while IFS= read -r f; do
  [ -f "$f" ] || continue
  checked=$((checked + 1))
  if [ "$(wc -c < "$f")" -ne "$(tr -d '\r' < "$f" | wc -c)" ]; then
    hits="${hits}    ${f}"$'\n'
  fi
done < <(printf '%s\n' \
  internal/platform/migrate/sql/mysql/*.sql \
  internal/platform/migrate/checksums.golden)

if [ "$checked" -eq 0 ]; then
  echo "LINT FAIL: 未找到任何字节校验文件（请在仓库根目录运行 scripts/check-eol.sh）"
  exit 1
fi

[ -z "$hits" ] && exit 0

echo "LINT FAIL: 字节校验文件必须是 LF（以下文件含 CRLF）"
printf '%s' "$hits"
echo "    -> 行尾会改变文件字节与 SHA-256：migrate up 会报 checksum 不匹配，Windows 构建的镜像与 CI 产物不兼容。"
echo "    -> 修复（保留内容改动，只重写行尾为 LF；在仓库根目录整段复制执行）："
echo "         git add --renormalize internal/platform/migrate/sql/mysql/ internal/platform/migrate/checksums.golden"
echo "         touch internal/platform/migrate/sql/mysql/*.sql internal/platform/migrate/checksums.golden"
echo "         git checkout-index -f -- internal/platform/migrate/sql/mysql/*.sql internal/platform/migrate/checksums.golden"
exit 1
