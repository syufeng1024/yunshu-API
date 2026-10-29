#!/usr/bin/env bash
# =============================================================================
#  云枢 API 中转站 · 从全量逻辑备份恢复
# =============================================================================
#  用法（仓库根目录）：
#      bash scripts/restore.sh backups/yunshu-YYYYmmdd-HHMMSS.sql.gz --db yunshu_drill
#      bash scripts/restore.sh backups/yunshu-YYYYmmdd-HHMMSS.sql.gz --db yunshu --yes
#
#  安全默认（T36 硬约束 3）：
#    1. 目标库默认 yunshu_drill；恢复到 yunshu 必须同时给 --db yunshu --yes。
#    2. 任何 *_test 库（含 yunshu_test / yunshu_recon_test）与系统库一律拒绝，
#       没有开关可以绕过。
#    3. 恢复前必须校验 dump 的 sha256 与同名 manifest 一致；manifest 缺失或
#       记录对不上就退出，不碰目标库。
#    4. 不执行任何迁移。dump 自带 schema_migrations；目标库版本低于 dump 版本
#       时报错退出（旧代码配新 Schema 会静默出错，宁可停下）。
#
#  口令：与 backup.sh 同法，MYSQL_PWD 只进环境变量，不进 mysql 的 argv。
#
#  幂等：dump 里每张表都是 DROP TABLE IF EXISTS + CREATE TABLE，重复恢复到
#        同一个 scratch 库会覆盖同名表。目标库里多出来的表不会被清理（见
#        docs/dr-plan.md 已知限制）。
#
#  退出码：0 恢复完成且行数校验无丢失；1 前置失败 / 校验失败 / 导入失败 /
#          恢复后行数少于 manifest 基线。
# =============================================================================

set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
ENV_FILE="${YUNSHU_ENV_FILE:-$ROOT_DIR/deploy/.env.stack}"

CONTAINER="${YUNSHU_MYSQL_CONTAINER:-yunshu-stack-mysql}"
PROTECTED_DB="${YUNSHU_DB_NAME:-yunshu}"
DEFAULT_DB="${YUNSHU_RESTORE_DB:-yunshu_drill}"

# 与 backup.sh / docs/dr-plan.md 保持一致。
KEY_TABLES="t_user t_credit_account t_credit_batch t_ledger_entry t_usage_record t_recharge_order"

DUMP=""
DB="$DEFAULT_DB"
MANIFEST=""
YES=0

MYSQL_ROOT_PASSWORD_VALUE=""

if [ -t 1 ]; then
  C_INFO=$'\033[36m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'; C_OFF=$'\033[0m'
else
  C_INFO=''; C_WARN=''; C_ERR=''; C_OFF=''
fi

info() { printf '%s[restore]%s %s\n' "$C_INFO" "$C_OFF" "$*"; }
warn() { printf '%s[restore] WARN%s %s\n' "$C_WARN" "$C_OFF" "$*" >&2; }
die()  { printf '%s[restore] FATAL%s %s\n' "$C_ERR" "$C_OFF" "$*" >&2; exit 1; }

usage() {
  awk 'NR > 1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
}

# ---------------------------------------------------------------------------
# 环境与 MySQL 访问
# ---------------------------------------------------------------------------

env_get() {
  local key="$1"
  [ -f "$ENV_FILE" ] || return 0
  awk -v k="$key" '
    index($0, k "=") == 1 {
      v = substr($0, length(k) + 2)
      sub(/[ \t]+#.*$/, "", v)
      gsub(/^[ \t]+|[ \t]+$/, "", v)
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) {
        v = substr(v, 2, length(v) - 2)
      }
      found = v
    }
    END { print found }
  ' "$ENV_FILE"
}

mysql_q() {
  MYSQL_PWD="$MYSQL_ROOT_PASSWORD_VALUE" docker exec -i -e MYSQL_PWD "$CONTAINER" \
    mysql --default-character-set=utf8mb4 -uroot -N -B -e "$1"
}

db_exists() {
  [ "$(mysql_q "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$1'")" = "1" ]
}

db_version() {
  local db="$1" has
  has=$(mysql_q "SELECT COUNT(*) FROM information_schema.tables
                  WHERE table_schema = '$db' AND table_name = 'schema_migrations'")
  if [ "$has" = "1" ]; then
    mysql_q "SELECT COALESCE(MAX(version), '') FROM \`$db\`.schema_migrations"
  fi
  return 0
}

db_table_count() {
  mysql_q "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '$1'"
}

table_exists() {
  [ "$(mysql_q "SELECT COUNT(*) FROM information_schema.tables
                 WHERE table_schema = '$1' AND table_name = '$2'")" = "1" ]
}

# manifest_get MANIFEST KEY FILE_BASENAME：只在这个 file= 所属的 [db ...] 块里取值。
manifest_get() {
  awk -v want="$3" -v key="$2" '
    /^\[db / { found = 0; next }
    /^file=/ {
      f = substr($0, 6)
      found = (f == want)
      next
    }
    found && index($0, key "=") == 1 { print substr($0, length(key) + 2); exit }
  ' "$1"
}

# numeric_version 0026 → 26；非纯数字返回 1（迁移版本只可能是 4 位数字）。
numeric_version() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  printf '%s' "$((10#$1))"
}

# ---------------------------------------------------------------------------
# 参数
# ---------------------------------------------------------------------------

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --db)
        [ $# -ge 2 ] || die "--db 需要值"
        DB="$2"; shift 2 ;;
      --yes)
        YES=1; shift ;;
      --manifest)
        [ $# -ge 2 ] || die "--manifest 需要值"
        MANIFEST="$2"; shift 2 ;;
      --help|-h)
        usage; exit 0 ;;
      -*)
        die "未知参数：$1（只支持 --db / --yes / --manifest / --help）" ;;
      *)
        [ -z "$DUMP" ] || die "只能指定一个 dump 文件：多余参数 '$1'"
        DUMP="$1"; shift ;;
    esac
  done
  [ -n "$DUMP" ] || { usage >&2; die "缺少 dump 文件"; }
}

# ---------------------------------------------------------------------------
# 安全校验
# ---------------------------------------------------------------------------

validate_target() {
  case "$DB" in
    ''|*[!A-Za-z0-9_]*) die "目标库名非法：'$DB'（只允许字母、数字、下划线）" ;;
  esac
  case "$DB" in
    mysql|sys|performance_schema|information_schema)
      die "拒绝恢复到系统库 $DB" ;;
    yunshu_test|yunshu_recon_test|*_test)
      die "拒绝恢复到测试库 $DB：T36 硬约束 2（测试库不属恢复范围，且没有任何开关可绕过）" ;;
  esac

  if [ "$DB" = "$PROTECTED_DB" ]; then
    # 这条警告即使带 --yes 也照打：--yes 只表示不再交互，不表示风险消失。
    warn "================================================================"
    warn " 目标库是生产主库 $DB：导入会 DROP 并重建 dump 里的每张表"
    warn " 导入期间必须已停止 gatewayd / controld / jobsd，避免恢复中被写入"
    warn " 建议先跑 scripts/backup.sh 保留现场，确认可回退后再执行本命令"
    warn "================================================================"
    [ "$YES" -eq 1 ] || die "拒绝覆盖 $DB：必须显式指定 --db $DB --yes"
  fi
}

verify_dump() {
  [ -f "$DUMP" ] || die "dump 文件不存在：$DUMP"
  [ -s "$DUMP" ] || die "dump 文件为空：$DUMP"

  [ -n "$MANIFEST" ] || MANIFEST="${DUMP%.sql.gz}.manifest"
  [ -f "$MANIFEST" ] || die "找不到 manifest：$MANIFEST（无法校验 sha256，拒绝恢复）"

  local actual expected base
  base=$(basename "$DUMP")
  actual=$(sha256sum "$DUMP" | cut -d' ' -f1)
  expected=$(manifest_get "$MANIFEST" sha256 "$base")
  [ -n "$expected" ] || die "manifest 里没有 $base 的记录（检查 --manifest 是否配对）"
  if [ "$actual" != "$expected" ]; then
    die "sha256 不匹配：文件 $actual，manifest $expected（dump 已损坏或被替换，拒绝恢复）"
  fi
  info "sha256 校验通过：$actual"

  # backup.sh 不带 --databases，所以正常 dump 里没有 USE / CREATE DATABASE。
  # 有的话导入会把语句重定向到别的库，必须先拒绝。
  local head_lines
  head_lines=$(gunzip -c "$DUMP" 2>/dev/null | head -n 100 || true)
  if printf '%s\n' "$head_lines" | grep -qE '^(USE |CREATE DATABASE)'; then
    die "dump 含 USE / CREATE DATABASE 语句：不是 scripts/backup.sh 的产物，拒绝恢复"
  fi

  DUMP_VERSION=$(manifest_get "$MANIFEST" schema_version "$base")
  DUMP_TABLES=$(manifest_get "$MANIFEST" table_count "$base")
  DUMP_BASENAME="$base"
}

check_target_version() {
  local target_version="" exists has_sm
  exists=$(mysql_q "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$DB'")
  if [ "$exists" = "1" ]; then
    has_sm=$(mysql_q "SELECT COUNT(*) FROM information_schema.tables
                       WHERE table_schema = '$DB' AND table_name = 'schema_migrations'")
    if [ "$has_sm" = "1" ]; then
      target_version=$(db_version "$DB")
    fi
  fi

  if [ -n "$target_version" ] && [ -n "$DUMP_VERSION" ]; then
    local tv dv
    tv=$(numeric_version "$target_version") || die "目标库 schema_migrations 版本非数字：'$target_version'"
    dv=$(numeric_version "$DUMP_VERSION") || die "manifest schema_version 非数字：'$DUMP_VERSION'"
    if [ "$tv" -lt "$dv" ]; then
      die "目标库 $DB 迁移版本 $target_version 低于 dump 版本 $DUMP_VERSION：先在本库执行迁移再恢复（本脚本不自动跑迁移）"
    fi
    info "版本检查通过：目标库 $target_version ≥ dump $DUMP_VERSION"
  elif [ "$exists" = "1" ] && [ -n "$DUMP_VERSION" ]; then
    info "目标库 $DB 无 schema_migrations：按空库处理，dump 会自带迁移表"
  fi

  if [ "$exists" != "1" ]; then
    info "目标库 $DB 不存在，创建（utf8mb4 / utf8mb4_0900_as_cs）"
    mysql_q "CREATE DATABASE \`$DB\` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs"
  fi
}

# ---------------------------------------------------------------------------
# 导入与校验
# ---------------------------------------------------------------------------

import_dump() {
  info "导入 $(basename "$DUMP") → $DB（不执行迁移）"
  if ! gunzip -c "$DUMP" | MYSQL_PWD="$MYSQL_ROOT_PASSWORD_VALUE" \
        docker exec -i -e MYSQL_PWD "$CONTAINER" \
        mysql --default-character-set=utf8mb4 -uroot "$DB"; then
    die "导入失败：$DB 可能处于部分导入状态（dump 里的表已 DROP）。修复后重跑本脚本覆盖，不要直接起服务"
  fi
}

post_check() {
  local tables version t expected actual status failed=0

  tables=$(db_table_count "$DB")
  version=$(db_version "$DB")

  printf '\n[restore] 目标库 %s\n' "$DB"
  printf '  表数量          %s\n' "$tables"
  printf '  schema_version  %s\n' "${version:-<无 schema_migrations>}"
  printf '\n  关键表行数（恢复后 / manifest 基线）\n'

  for t in $KEY_TABLES; do
    expected=$(manifest_get "$MANIFEST" "rows.$t" "$DUMP_BASENAME")
    [ -n "$expected" ] || continue
    if ! table_exists "$DB" "$t"; then
      printf '    %-24s %12s / %12s  MISSING\n' "$t" "-" "$expected"
      failed=1
      continue
    fi
    actual=$(mysql_q "SELECT COUNT(*) FROM \`$DB\`.\`$t\`")
    if [ "$actual" -lt "$expected" ]; then
      status="LOST"
      failed=1
    elif [ "$actual" -gt "$expected" ]; then
      status="NEW（导出后源库有新写入）"
    else
      status="OK"
    fi
    printf '    %-24s %12s / %12s  %s\n' "$t" "$actual" "$expected" "$status"
  done

  if [ -n "$DUMP_TABLES" ] && [ "$tables" -lt "$DUMP_TABLES" ]; then
    printf '\n  [FAIL] 表数量 %s < manifest %s\n' "$tables" "$DUMP_TABLES"
    failed=1
  fi

  if [ "$failed" -ne 0 ]; then
    printf '\n[restore] FAILED — 恢复结果少于 manifest 基线，按失败处理\n'
    return 1
  fi
  printf '\n[restore] OK — 恢复完成，行数不低于 manifest 基线\n'
  return 0
}

# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------

main() {
  local started=$SECONDS
  parse_args "$@"

  [ -f "$ENV_FILE" ] || die "找不到环境文件：$ENV_FILE"
  command -v docker >/dev/null 2>&1 || die "PATH 里没有 docker"
  command -v gunzip >/dev/null 2>&1 || die "PATH 里没有 gunzip"
  command -v sha256sum >/dev/null 2>&1 || die "PATH 里没有 sha256sum"

  MYSQL_ROOT_PASSWORD_VALUE=$(env_get MYSQL_ROOT_PASSWORD)
  [ -n "$MYSQL_ROOT_PASSWORD_VALUE" ] || die "$ENV_FILE 里没有 MYSQL_ROOT_PASSWORD"

  local running
  running=$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || true)
  [ "$running" = "true" ] || die "容器 $CONTAINER 不存在或未运行"
  [ "$(mysql_q 'SELECT 1')" = "1" ] || die "无法连接 $CONTAINER 上的 MySQL"

  validate_target
  verify_dump
  check_target_version

  info "开始恢复 → $DB"
  import_dump

  if ! post_check; then
    exit 1
  fi

  info "耗时 $((SECONDS - started))s（含 sha256 校验与导入）"
  if [ "$DB" = "$PROTECTED_DB" ]; then
    info "生产库已恢复：启动服务前确认 8 条对账断言与 /healthz /readyz（见 docs/dr-plan.md §4）"
  fi
}

main "$@"
