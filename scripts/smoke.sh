#!/usr/bin/env bash
# smoke.sh — 部署后冒烟自检：跑完给出 PASS/FAIL 计数，失败时以非 0 退出。
#
# 与 scripts/stack.sh smoke 的分工：
#   stack.sh smoke   只把迁移版本、表数量、探针状态打印出来，人肉看
#   smoke.sh（本脚本）  同样几项 + 容器状态 + 内网门鉴权 + /console/ 回归，
#                     每条都判定，失败计数非 0 就 exit 1——可以挂进 CI，
#                     也可以在发版脚本里当最后一道闸。
#
# 为什么不写成 make 目标：本机（Windows / Git Bash）没有 GNU make，
# `make stack-smoke` 直接 command not found。Makefile 的 stack-smoke 现在
# 反过来调用本脚本，两条入口共用同一份断言。
#
# 用法（仓库根目录）：
#   scripts/smoke.sh              # 默认
#   scripts/smoke.sh --quiet      # 只打结论与失败项
#   SMOKE_RETRIES=5 scripts/smoke.sh
#
# 退出码：0 全通过（允许 WARN）；1 有 FAIL；2 前置条件不满足。
#
# 检查项与判据：
#   1. 前置       docker 可用、deploy/.env.stack 存在、编排文件存在
#   2. 容器       mysql/redis/gatewayd/controld/jobsd/web running+healthy；
#                 migrate 必须 exited(0)——它是跑完就退的一次性任务
#   3. 迁移       schema_migrations 最新版本非空、表数量 > 0
#   4. 探针       gatewayd /healthz /readyz、controld 同、web / /healthz
#   5. 回归       web /console/ 与 /admin/ 必须 200：它们是 SPA 页面路由，
#                 一旦被 nginx 当 /api/ 代理出去就变成纯文本 404
#                 （2026-10-29 实测踩过，commit 1381496 修的就是这个）
#   6. 内网门     gatewayd /internal/v1/chat/completions 无签名必须 401。
#                 401 = 端点已注册且验签生效；404 = INTERNAL_CALL_SECRET
#                 为空、端点根本没注册（T27 调试台会 503）。
#                 同时 web 站点上的 /internal/ 必须 404——nginx 第二道门，
#                 不能让人从公网把请求送到验签代码面前。
#
# 只依赖 bash + docker + curl + 少量 coreutils，不引入 jq / make / nc。

set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2

STACK_COMPOSE="deploy/docker-compose.stack.yml"
STACK_ENV="deploy/.env.stack"

WEB_PORT_DEFAULT=8088
GATEWAY_PORT_DEFAULT=18080
CONTROL_PORT_DEFAULT=18081

SMOKE_RETRIES="${SMOKE_RETRIES:-3}"
CURL_TIMEOUT="${CURL_TIMEOUT:-5}"

QUIET=0
case "${1:-}" in
  --quiet|-q) QUIET=1 ;;
  ''|--help|-h)
    if [ "${1:-}" != "" ]; then
      sed -n '2,42p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
    fi
    ;;
  *)
    echo "未知参数：$1（只接受 --quiet）" >&2
    exit 2
    ;;
esac

ok=0
bad=0
warned=0

pass() {
  ok=$((ok + 1))
  [ "$QUIET" -eq 1 ] || printf '  [PASS] %s\n' "$1"
}

fail() {
  bad=$((bad + 1))
  printf '  [FAIL] %s\n' "$1"
  if [ -n "${2:-}" ]; then
    printf '%s\n' "$2" | sed 's/^/         /'
  fi
}

warn() {
  warned=$((warned + 1))
  printf '  [WARN] %s\n' "$1"
  if [ -n "${2:-}" ]; then
    printf '%s\n' "$2" | sed 's/^/         /'
  fi
}

section() {
  [ "$QUIET" -eq 1 ] || printf '\n%s\n' "--- $1 ---"
}

# 读取 .env.stack 里的键；空值回落到默认。
env_get() {
  local key="$1" fallback="$2" v
  if [ -f "$STACK_ENV" ]; then
    v="$(grep -E "^${key}=" "$STACK_ENV" 2>/dev/null | tail -1 | cut -d= -f2- || true)"
    if [ -n "${v}" ]; then
      printf '%s' "$v"
      return 0
    fi
  fi
  printf '%s' "$fallback"
}

# docker inspect 一行取状态：status|health|exitcode，容器不存在时返回空。
container_state() {
  docker inspect -f '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}-{{end}}|{{.State.ExitCode}}' "$1" 2>/dev/null
}

# 期望 running + healthy。缺失/不健康都算 FAIL：冒烟是最后一道闸，
# 不能因为"容器还在 starting"就放过。
check_healthy() {
  local name="$1" st health code
  local raw
  raw="$(container_state "$name")"
  if [ -z "$raw" ]; then
    fail "$name 不存在（栈未启动？先跑 scripts/stack.sh up）"
    return
  fi
  st="${raw%%|*}"
  health="$(printf '%s' "$raw" | cut -d'|' -f2)"
  if [ "$st" = "running" ] && [ "$health" = "healthy" ]; then
    pass "$name running/healthy"
  else
    fail "$name 状态异常：status=$st health=$health" "看日志：docker logs --tail=50 $name"
  fi
}

# HTTP 探针：code 命中期望集合算通过。000（连不上）与 5xx 会重试，
# 避免刚 up 完、nginx 还没热起来的瞬间抖动被记成失败。
http_check() {
  local name="$1" url="$2"
  shift 2
  local expects="$*"
  local code attempt=1
  while : ; do
    code="$(curl -s -o /dev/null -m "$CURL_TIMEOUT" -w '%{http_code}' "$url" 2>/dev/null)"
    [ -n "$code" ] || code="000"
    case "$code" in
      000|5??) ;;
      *) break ;;
    esac
    [ "$attempt" -ge "$SMOKE_RETRIES" ] && break
    attempt=$((attempt + 1))
    sleep 1
  done
  if [[ " $expects " == *" $code "* ]]; then
    pass "$name → $code"
  else
    fail "$name → $code（期望 $expects）" "$url"
  fi
}

# ---------------------------------------------------------------- 1. 前置
section "前置条件"

if ! docker version --format '{{.Server.Version}}' >/dev/null 2>&1; then
  echo "  [FAIL] docker 不可用（Docker Desktop 没启动？）"
  exit 2
fi
pass "docker $(docker version --format '{{.Server.Version}}' 2>/dev/null)"

if [ ! -f "$STACK_ENV" ]; then
  echo "  [FAIL] $STACK_ENV 缺失，先跑：scripts/stack.sh env"
  exit 2
fi
pass "$STACK_ENV 存在"

if [ ! -f "$STACK_COMPOSE" ]; then
  echo "  [FAIL] $STACK_COMPOSE 缺失"
  exit 2
fi

MYSQL_PWD_VALUE="$(env_get MYSQL_ROOT_PASSWORD root)"
# 端口优先级：显式环境变量 > .env.stack > 默认值。显式传入是为了能把脚本
# 指向另一套栈（比如调试用端口），也让负向测试不必改 env 文件。
WEB_PORT="${WEB_PORT:-$(env_get WEB_PORT "$WEB_PORT_DEFAULT")}"
GATEWAY_PORT="${GATEWAY_PORT:-$(env_get GATEWAY_PORT "$GATEWAY_PORT_DEFAULT")}"
CONTROL_PORT="${CONTROL_PORT:-$(env_get CONTROL_PORT "$CONTROL_PORT_DEFAULT")}"

# ---------------------------------------------------------------- 2. 容器
section "容器状态"

check_healthy yunshu-stack-mysql
check_healthy yunshu-stack-redis
check_healthy yunshu-stack-gatewayd
check_healthy yunshu-stack-controld
check_healthy yunshu-stack-jobsd
check_healthy yunshu-stack-web

# migrate 是一次性任务：跑完退出 0 才算正常，running 反而说明它卡住了。
raw="$(container_state yunshu-stack-migrate)"
if [ -z "$raw" ]; then
  fail "yunshu-stack-migrate 不存在（一次性迁移容器跑完被清理？检查 up 的输出）"
else
  st="${raw%%|*}"
  code="$(printf '%s' "$raw" | cut -d'|' -f3)"
  if [ "$st" = "exited" ] && [ "$code" = "0" ]; then
    pass "yunshu-stack-migrate exited(0)"
  else
    fail "yunshu-stack-migrate 异常：status=$st exit=$code" "看日志：docker logs yunshu-stack-migrate"
  fi
fi

# ---------------------------------------------------------------- 3. 迁移
section "数据库迁移"

# 用 MYSQL_PWD 传口令，避免 `-p<pw>` 在命令行暴露、也免掉 "insecure" 警告。
db_query() {
  docker exec -e MYSQL_PWD="$MYSQL_PWD_VALUE" yunshu-stack-mysql \
    mysql -uroot -N -e "$1" 2>/dev/null
}

latest="$(db_query "SELECT version FROM yunshu.schema_migrations ORDER BY version DESC LIMIT 1")"
if [ -n "$latest" ]; then
  applied="$(db_query "SELECT COUNT(*) FROM yunshu.schema_migrations")"
  pass "最新迁移版本 $latest（已应用 $applied 条）"

  # 本地文件数 >= 已应用数是常态：0019 是条件种子，ENV!=local 时跳过且不记录，
  # 所以这里只 WARN，不判失败。
  files=0
  for f in internal/platform/migrate/sql/mysql/*.up.sql; do
    [ -f "$f" ] && files=$((files + 1))
  done
  if [ "$files" -gt 0 ] && [ "$applied" -lt "$files" ]; then
    warn "已应用 $applied 条 < 本地 $files 个迁移文件" \
      "若差的是 0019（条件种子，ENV!=local 时不记录）属正常；其余情况说明有迁移未应用"
  fi
else
  fail "查不到 yunshu.schema_migrations" "迁移可能没跑成功，看：docker logs yunshu-stack-migrate"
fi

tables="$(db_query "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='yunshu'")"
if [ -n "$tables" ] && [ "$tables" -gt 0 ] 2>/dev/null; then
  pass "yunshu 库 $tables 张表"
else
  fail "yunshu 库表数量异常：${tables:-<空>}"
fi

# 0022 起的请求正文落库：表在但 LOG_BODY_KEK 为空时正文不写，属配置选择；
# 表不在则是迁移没跑到 0022，会让日志详情页的正文区块直接报错。
if [ -n "$tables" ] && [ "$tables" -gt 0 ] 2>/dev/null; then
  body_table="$(db_query "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='yunshu' AND table_name='t_request_body'")"
  if [ "${body_table:-0}" = "1" ]; then
    if [ -n "$(env_get LOG_BODY_KEK '')" ]; then
      pass "t_request_body 已建 + LOG_BODY_KEK 已配（正文可回看）"
    else
      warn "t_request_body 已建，但 LOG_BODY_KEK 为空" \
        "正文不落库，请求不受影响；要在调试台回看正文就补 LOG_BODY_KEK 后重启 gatewayd/controld"
    fi
  else
    fail "t_request_body 不存在（迁移未到 0022）"
  fi
fi

# ---------------------------------------------------------------- 4. 探针
section "HTTP 探针"

http_check "gatewayd /healthz" "http://127.0.0.1:${GATEWAY_PORT}/healthz" 200
http_check "gatewayd /readyz " "http://127.0.0.1:${GATEWAY_PORT}/readyz"  200
http_check "controld /healthz" "http://127.0.0.1:${CONTROL_PORT}/healthz" 200
http_check "controld /readyz " "http://127.0.0.1:${CONTROL_PORT}/readyz"  200
http_check "web      /        " "http://127.0.0.1:${WEB_PORT}/"           200
http_check "web      /healthz " "http://127.0.0.1:${WEB_PORT}/healthz"    200

# 控制面 API 前缀必须通到 controld。两条一起看才说明链路是通的：
#   公开端点 200  → nginx 的 ^~ /api/ 反代到 controld 了
#   鉴权端点 401  → 请求真的进了 mux（不是兜底 404），只是没过鉴权
# 只用其中一条都会误判：401 也可能来自网关层统一拦截，200 又不覆盖鉴权分支。
http_check "web      /api/public/plans   " "http://127.0.0.1:${WEB_PORT}/api/public/plans" 200
http_check "web      /api/console/overview (未登录)" "http://127.0.0.1:${WEB_PORT}/api/console/overview" 401

# ---------------------------------------------------------------- 5. 回归
section "前端路由回归"

# /console/ 与 /admin/ 是 SPA 页面路由。历史上 nginx 用 ^/(api|console|admin)/
# 把它们一起代理给 controld，结果是纯文本 404、控制台 9 页全打不开。
# 这两条断言就是那个 bug 的守卫：200 才说明走的是 try_files → index.html。
http_check "web      /console/ " "http://127.0.0.1:${WEB_PORT}/console/" 200
http_check "web      /admin/   " "http://127.0.0.1:${WEB_PORT}/admin/"   200

# ---------------------------------------------------------------- 6. 内网门
section "内网入口（T27）"

# 无签名访问：401 = 端点注册且验签生效；404 = INTERNAL_CALL_SECRET 为空、
# 端点没注册，此时调试台每条请求都会 503 config_unavailable。
code="$(curl -s -o /dev/null -m "$CURL_TIMEOUT" -w '%{http_code}' \
  -X POST "http://127.0.0.1:${GATEWAY_PORT}/internal/v1/chat/completions" \
  -H 'Content-Type: application/json' -d '{}' 2>/dev/null)"
case "${code:-000}" in
  401) pass "gatewayd /internal/ 无签名 → 401（端点已注册）" ;;
  404) fail "gatewayd /internal/ → 404（端点未注册）" \
         "deploy/.env.stack 里 INTERNAL_CALL_SECRET 为空或与 SESSION_SECRET/CHANNEL_KEY_KEK/LOG_BODY_KEK 复用；补齐后重启 gatewayd 与 controld" ;;
  000) fail "gatewayd /internal/ 连不上（000）" ;;
  *)   fail "gatewayd /internal/ → $code（期望 401）" \
         "200/5xx 都说明验签没生效，先查 gatewayd 启动日志有没有 internal 相关 warn" ;;
esac

# nginx 第二道门：公网侧 /internal/ 必须直接 404，不能落到验签代码面前。
code="$(curl -s -o /dev/null -m "$CURL_TIMEOUT" -w '%{http_code}' \
  -X POST "http://127.0.0.1:${WEB_PORT}/internal/v1/chat/completions" \
  -H 'Content-Type: application/json' -d '{}' 2>/dev/null)"
if [ "${code:-000}" = "404" ]; then
  pass "web /internal/ → 404（nginx 拦截生效）"
else
  fail "web /internal/ → ${code:-000}（期望 404）" \
    "deploy/nginx/default.conf 的 ^~ /internal/ 块缺失或被别的 location 抢了"
fi

# ---------------------------------------------------------------- 汇总
printf '\n'
if [ "$bad" -ne 0 ]; then
  printf 'smoke: FAILED — %d 通过 / %d 失败 / %d 警告\n' "$ok" "$bad" "$warned"
  printf '站点 http://localhost:%s ｜ 网关 :%s ｜ 控制面 :%s\n' "$WEB_PORT" "$GATEWAY_PORT" "$CONTROL_PORT"
  printf '日志：scripts/stack.sh logs，或 docker logs --tail=100 yunshu-stack-gatewayd\n'
  exit 1
fi

printf 'smoke: OK — %d 通过 / 0 失败 / %d 警告\n' "$ok" "$warned"
printf '站点 http://localhost:%s ｜ 网关 :%s ｜ 控制面 :%s\n' "$WEB_PORT" "$GATEWAY_PORT" "$CONTROL_PORT"
exit 0
