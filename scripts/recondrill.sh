#!/usr/bin/env bash
# =============================================================================
#  云枢 API 中转站 · 在演练库上执行 R-01..R-08
# =============================================================================
#  作用：把 cmd/recondrill 编译成 linux 二进制，在 `yunshu-stack_backend`
#        网络里的一次性容器中执行，DSN 指向 compose 的 MySQL（服务名 mysql）
#        与目标库（默认 yunshu_drill）。
#
#  为什么不在宿主直接跑：`yunshu-stack-mysql` 不发布宿主端口（NetworkMode =
#  yunshu-stack_backend），宿主 13306 是另一台 `yunshu-mysql`（测试库所在）。
#  用 `--network yunshu-stack_backend` 的容器才能解析 `mysql:3306`。
#
#  口令：与 backup.sh / restore.sh 同法，从 deploy/.env.stack 读取后写入 0600
#        临时 env-file，经 `docker run --env-file` 注入；口令不出现在任何
#        argv（ps 看不到），退出时删除。
#
#  只读边界：recondrill 只写 t_reconcile_diff / t_notification（与 jobsd 的
#  日对账完全一致），不写余额、流水、批次。0 差异时不写任何行。
#
#  用法（仓库根目录）：
#      bash scripts/recondrill.sh                    # yunshu_drill，业务日=今天(CST)
#      bash scripts/recondrill.sh --db yunshu_drill -day 2026-10-29
#      bash scripts/recondrill.sh --allow-production --db yunshu -day 2026-10-29
#
#  退出码：0 全部断言 0 差异；1 有差异 / 前置失败；125 容器启动失败。
# =============================================================================

set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
ENV_FILE="${YUNSHU_ENV_FILE:-$ROOT_DIR/deploy/.env.stack}"

NETWORK="${YUNSHU_DRILL_NETWORK:-yunshu-stack_backend}"
MYSQL_SERVICE="${YUNSHU_DRILL_MYSQL_SERVICE:-mysql}"
# mysql:8.0 是栈内已有镜像；静态二进制只需一个带 /bin/sh 的镜像即可。
IMAGE="${YUNSHU_DRILL_IMAGE:-mysql:8.0}"

DB="${YUNSHU_DRILL_DB:-yunshu_drill}"
ALLOW_PRODUCTION=0
TOOL_ARGS=()

die() { printf '[recondrill] FATAL %s\n' "$*" >&2; exit 1; }

usage() {
  awk 'NR > 1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --db)
      [ $# -ge 2 ] || die "--db 需要值"
      DB="$2"; shift 2 ;;
    --allow-production)
      ALLOW_PRODUCTION=1; TOOL_ARGS+=("$1"); shift ;;
    --help|-h)
      usage; exit 0 ;;
    *)
      TOOL_ARGS+=("$1"); shift ;;
  esac
done

case "$DB" in
  ''|*[!A-Za-z0-9_]*) die "目标库名非法：'$DB'" ;;
  *_test|mysql|sys|performance_schema|information_schema)
    die "拒绝在 $DB 上运行：测试库 / 系统库不在演练范围" ;;
  yunshu)
    [ "$ALLOW_PRODUCTION" -eq 1 ] || die "拒绝在 yunshu 上运行：真实故障恢复后需显式 --allow-production" ;;
esac

[ -f "$ENV_FILE" ] || die "找不到环境文件：$ENV_FILE"
command -v docker >/dev/null 2>&1 || die "PATH 里没有 docker"
command -v go >/dev/null 2>&1 || die "PATH 里没有 go"

env_get() {
  local key="$1"
  awk -v k="$key" '
    index($0, k "=") == 1 {
      v = substr($0, length(k) + 2)
      sub(/[ \t]+#.*$/, "", v)
      gsub(/^[ \t]+|[ \t]+$/, "", v)
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) { v = substr(v, 2, length(v) - 2) }
      found = v
    }
    END { print found }
  ' "$ENV_FILE"
}

PW=$(env_get MYSQL_ROOT_PASSWORD)
[ -n "$PW" ] || die "$ENV_FILE 里没有 MYSQL_ROOT_PASSWORD"

WORK=$(mktemp -d)
cleanup() { rm -rf -- "$WORK"; }
trap cleanup EXIT

BIN="$WORK/recondrill"
ENV_TMP="$WORK/drill.env"
chmod 700 "$WORK"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BIN" "$ROOT_DIR/cmd/recondrill" \
  || die "go build ./cmd/recondrill 失败"

# DSN 走 env-file；口令含 = 或空格时 mysql.Config 需要它原样保留，这里只拒绝换行。
case "$PW" in
  *$'\n'*) die "MYSQL_ROOT_PASSWORD 含换行，无法写入 env-file" ;;
esac
printf 'ENV=local\nDB_DSN=root:%s@tcp(%s:3306)/%s?parseTime=true&loc=UTC&charset=utf8mb4\n' \
  "$PW" "$MYSQL_SERVICE" "$DB" >"$ENV_TMP"
chmod 600 "$ENV_TMP"

# Windows 宿主需要绝对 Windows 路径；Linux/macOS 原样使用。
win_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
}

docker run --rm \
  --network "$NETWORK" \
  --env-file "$(win_path "$ENV_TMP")" \
  -v "$(win_path "$BIN"):/recondrill:ro" \
  "$IMAGE" \
  sh -c 'cp /recondrill /tmp/recondrill && chmod +x /tmp/recondrill && exec /tmp/recondrill "$@"' \
  _ "${TOOL_ARGS[@]}"
